# Flatline · 会话优先重构设计 v1

> 2026-08-22 · 依据：本地真实数据（874 个会话 / 41.9 万事件 / 153 个资产，其中仅 136 个会话产生过资产相关任务记录）
> 配套 ADR：`docs/adr/018-friction-first-session-understanding.md`
> 本文是三条并行工作线（A 数据层与性能 / B 会话管理与总览前端 / C 摩擦分类与摩擦页）的共同契约。任何一方要改契约，先改本文再改代码。

## 0. 为什么改

1. **资产视角在真实数据上几乎空转。** 874 个会话里只有 136 个能对应到某个资产的"相关任务"，146/153 个资产长期处于 `no_opportunity`。监护仪没有错，但它大部分时间无事可报。
2. **摩擦在真实数据里大量存在。** 工具调用出错、非零退出、用户中断，在每个项目、每个 harness 下都有明确记录，而且每一条都能回到原始会话定位。
3. **会话本身是用户最大的资产。** 几百个会话已经被本地化、脱敏聚合在一套系统里，但现在只是一张铺满屏幕的列表：没有项目维度、没有时间范围、没有标签、没有服务端搜索、没有用户自己的标注。
4. **慢。** 每次路由切换都重新拉 4 个全量接口；daemon 重启要先把 3.7 GB 原生历史重新解析完才开始监听；数据库单连接，后台刷新期间所有 API 排队。

因此：**从"按资产迭代"转向"从摩擦出发、把会话历史整理好"**。资产监护保留，降为一级页之一；总览、会话、摩擦成为主路径。

## 1. 现状诊断（性能）

| # | 症状 | 根因 | 位置 |
| --- | --- | --- | --- |
| P1 | 每个页面点击都要转圈 | `route()` 每次先 `loadOverview()`：assets(≤5000)+stats+notifications(≤5000)+sessions(≤5000) 四个全量接口；且 assets 在 `wall`/`summary` 两种模式间切换时缓存失效，资产页⇄其他页来回每次都重拉 | `app.js:1077,2043` |
| P2 | daemon 启动后很久打不开页面 | `refresh()` 同步跑完（资产扫描 + 解析全部原生 JSONL + 逐会话 ingest + 全部资产评估）才 `net.Listen` | `cmd/flatline/main.go:runDaemon` |
| P3 | 重启一次就重新解析 3.7 GB | 文件指纹 `nativeFiles` 只在内存，进程重启即丢 | `runtime/app.go:ImportNativeHistory` |
| P4 | 后台刷新期间所有请求卡住 | `SetMaxOpenConns(1)`：API 读请求与 ingest 写事务共用唯一连接 | `storage/storage.go:Open` |
| P5 | 会话列表慢 | 每个会话 3 个关联子查询扫 `events`（874 × 3 次）；无会话级统计投影 | `api.go:handleSessions` |
| P6 | 资产墙慢 | 每个资产 8 个查询 + `os.Stat`，153 个资产串行 ≈ 1200+ 次查询 | `api.go:listWallAssets/assetFactsWithMarkers` |
| P7 | `/stats` 每次全表 GROUP BY | `substr(occurred_at,1,10)` 无索引可用，41.9 万行全扫；而 P1 让它每次路由都被调用 | `api.go:handleStats` |
| P8 | 摩擦总览 4 次全表扫描 | CTE 对 `events` 扫 `asset_violation`（`event_type` 无索引），`LIKE` 扫 `payload_json`；summary/count/groups/projects 各跑一遍 | `api/friction.go` |
| P9 | 通知接口逐条查询 | 每条 transition 再查 session | `api.go:handleNotifications` |
| P10 | 浏览器无缓存 | 无 ETag / 无 data_version，无法 304 | 全部 API |
| P11 | 每隔几分钟"什么都在加载" | `refresh()` 每 5 分钟重跑：扫描 ~/.codex 下 779 个资产 + `EvaluateAll` 对 938 个资产逐个评估（实测 **2 分 13 秒**），期间独占唯一数据库连接，所有 API 排队 | `runtime/app.go:EvaluateAll`、`main.go` |

### 1.1 实测基线（2026-08-22，本机真实历史：1082 个会话 / 35.9 万事件 / 938 个资产，DB 860 MB）

| 项 | 实测 |
| --- | --- |
| 冷启动到开始监听 | **5 分 10 秒**（资产扫描 40 s → 历史解析+ingest 2 分 22 秒 → 资产评估 2 分 13 秒） |
| `/api/v1/sessions?limit=5000` | 0.39 s / 679 KB |
| `/api/v1/assets?view=wall` | 0.39 s / 1.6 MB |
| `/api/v1/stats` | 0.19 s |
| `/api/v1/timeline?limit=5000` | 0.40 s / **8.9 MB** |
| `/api/v1/friction`（总览） | 0.53 s |
| 每次切页前端实际下载 | ≈ 2.9 MB（sessions + wall assets + stats + notifications），串行在单连接上 ≈ 1 s，再加解析/渲染 5000 行 |

结论：单个接口不算极慢，慢在**每次切页全量拉 + 单连接排队 + 每 5 分钟被 2 分钟的评估占住连接 + 重启 5 分钟不可用**。

A 线在 §3.2 之外还必须做：**评估增量化**——`EvaluateAll` 只评估本轮有新事件/新版本/新引用检查的资产（其它资产状态不可能变化），本轮没有任何新 ingest 时整轮跳过；评估与扫描都不得持有长事务。

> **A 实施补充（2026-08-22）之二——引用检查去重（增量化的前提）**：原来每轮资产扫描都会给每个资产版本写一条新的 `reference_checks`（`checked_at` 每轮不同），结果是「每个资产每轮都有新引用检查」，增量评估会退化成全量。现在只有当 `overall_status` 或提取出的引用集合（kind/value/存在与否）与该资产版本上一条检查**不同**时才写新行；完全一样就不写。这不是丢弃观察：同样的观察不构成新的观察。
>
> **A 实施补充（2026-08-22）**：增量触发集为「新事件 / 新资产版本 / 新引用检查 / 新 opportunity / 新 participation」五者的并集；这五张表都用行 id 高水位线比较。另加一条例外：**每进程首轮、以及距上次全量评估满 1 小时时跑一次全量**（`runtime.FullEvaluationInterval`）。原因：`dormant` 判定只依赖「资产年龄 + 累计参与数」，不依赖任何新事实——纯事实驱动的触发条件下，一个从来没人用的资产永远不会被重新判定，也就永远进不了 dormant。全量频率因此从每 5 分钟降到每小时，而不是完全取消。日志逐轮打印 `full=/evaluated=/skipped=/reason=`。

## 2. 新的一级信息架构

```text
总览  /  会话  /  摩擦  /  资产  /  变化时间线
```

| 页面 | 回答 | 主维度 |
| --- | --- | --- |
| 总览 `#/` | 这段时间我在哪些项目、用哪个 harness、做了多少会话、摩擦集中在哪里 | 时间范围 × 项目 |
| 会话 `#/sessions` | 找到并管理某一次会话：筛选、分组、排序、搜索、标注 | 项目 / harness / 时间 / 标签 |
| 摩擦 `#/friction` | 哪个项目在什么 harness 下出现了哪类明确记录的摩擦，每一次发生在哪个会话 | 项目 × harness × 类别 |
| 资产 `#/assets` | 资产生命体征墙（原首页，整体保留，不再是默认页） | 资产 |
| 变化时间线 `#/timeline` | 不变 | 时间 |

原 `#/stats` 的热力图与分布并入总览；`#/stats` 路由保留为总览的"详细统计"锚点（不单独维护新功能）。侧栏"项目"分组从"资产作用域"改为**真实项目列表**（按会话 `cwd` 聚合），点击即带 `project=` 进入会话页。

证据纪律不变：缺失显示"未记录"，不写因果，不出总分。

## 3. 数据层设计（A 负责）

### 3.1 迁移 `006_session_management.sql`

```sql
-- 原生文件指纹持久化：重启后跳过未变化文件（解决 P3）
CREATE TABLE native_files (
  path          TEXT PRIMARY KEY,
  size          INTEGER NOT NULL,
  mtime_ns      INTEGER NOT NULL,
  session_id    TEXT,                      -- 解析出的 session（可空：损坏/不属于项目）
  last_read_at  TEXT NOT NULL
);

-- 会话级统计投影（可整体重算，ADR-10；解决 P5）
CREATE TABLE session_stats (
  session_id         TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  event_count        INTEGER NOT NULL,
  transcript_count   INTEGER NOT NULL,
  message_count      INTEGER NOT NULL,     -- transcript_message
  user_message_count INTEGER NOT NULL,     -- role=user
  tool_call_count    INTEGER NOT NULL,
  tool_result_count  INTEGER NOT NULL,
  friction_count     INTEGER NOT NULL,     -- friction_records 去重数 + asset_violation
  tool_error_count   INTEGER NOT NULL,
  nonzero_exit_count INTEGER NOT NULL,
  asset_count        INTEGER NOT NULL,
  first_event_at     TEXT,
  last_event_at      TEXT,
  duration_ms        INTEGER,              -- ended-started；任一缺失则 NULL（未记录）
  computed_at        TEXT NOT NULL
);

-- 会话标签：规则标签（task/workspace）由 daemon 写，用户标签由用户写
CREATE TABLE session_tags (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  tag        TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('task','workspace','user')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (session_id, tag, kind)
);
CREATE INDEX idx_session_tags_tag ON session_tags (tag, kind);

-- 用户标注：置顶与笔记（只写本地 DB，不触碰任何源文件）
CREATE TABLE session_annotations (
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  pinned     INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0,1)),
  note       TEXT,
  updated_at TEXT NOT NULL
);

-- 索引（解决 P7/P8/P5）
CREATE INDEX idx_events_type_session ON events (event_type, session_id);
CREATE INDEX idx_events_occurred ON events (occurred_at);
CREATE INDEX idx_sessions_started ON sessions (started_at);
CREATE INDEX idx_sessions_cwd ON sessions (cwd);
CREATE INDEX idx_sessions_source_started ON sessions (source, started_at);
CREATE INDEX idx_friction_records_occurred ON friction_records (occurred_at);

-- 搜索：会话级 trigram FTS（中英文子串都能命中）
CREATE VIRTUAL TABLE sessions_fts USING fts5(
  session_id UNINDEXED, title, task_text, cwd, model, source_session_id,
  tokenize = 'trigram'
);
-- 会话正文 FTS：只收 transcript_message（user/assistant 文本），external content 指向 events.id
CREATE VIRTUAL TABLE events_fts USING fts5(
  text, content='', tokenize='trigram'
);
```

- `sessions_fts` 在 `IngestSession` 后 upsert（先 delete 再 insert）；迁移时全量回填。
- `events_fts` 以 `rowid = events.id` 写入，只在事件**新插入**时写（`INSERT ... RETURNING id`），迁移时回填已有 `transcript_message`。A 必须实测索引体积并写进交付报告；若 > 300 MB，退化为只索引 `role='user'` 的消息并在本文记录。
- `storage.SchemaVersion` 由 A 置为 **7**（C 的 007 见 §5）。

> **A 实施补充（2026-08-22）**：
> - **实测体积**：本机 897 会话 / 45 070 条 `transcript_message`（正文合计 16.6 MB）下，`events_fts` 占 **64.1 MB**、`sessions_fts` 占 2.6 MB，远低于 300 MB 阈值，因此保留「索引全部 `transcript_message`」不退化。整库 860 MB → 977 MB。
> - **多加一个索引** `idx_events_day ON events (substr(occurred_at,1,10))`。§3.1 原列的 `idx_events_occurred` 解决不了 P7：`/stats` 与总览的日聚合 `GROUP BY substr(occurred_at,1,10)` 是表达式分组，普通 `occurred_at` 索引用不上。
> - `session_stats.friction_count` = `friction_records` 中该会话的 **distinct `source_event_id` 数** + `asset_violation` 事件数（同一事件被分成多个 `friction_kind` 时只算一次）。

### 3.2 daemon 行为

1. **先监听，后导入。** `runDaemon` 先 `net.Listen` 并启动 HTTP，再在 goroutine 里跑首轮 `refresh()`；UI 通过 `/api/v1/ingest/status` 看进度。
2. **文件指纹持久化。** 启动时从 `native_files` 载入 `KnownFiles`；每轮写回。未变化文件不再读。
3. **会话统计在 ingest 末尾重算该会话一行**（`RecomputeSessionStats(sessionID)`），另提供 `RecomputeAllSessionStats()` 供迁移后首启与 `flatline scan` 使用。
4. **规则标签落库。** `nativeTaskTags` 的结果以 `kind='task'`（analysis/implementation/…）与 `kind='workspace'`（`workspace-<slug>`）写入 `session_tags`（先删该会话的非 user 标签再写）。
5. **连接池。** `SetMaxOpenConns(4)`（WAL 读写并发；写事务之间靠 `busy_timeout`）。ingest 仍然是唯一写者，长事务保持按会话粒度。
6. **data_version。** `App` 持有一个单调递增的 `dataVersion`（每轮 refresh 完成 +1，用户标注写入 +1）。API 层有一个以 `dataVersion` 为键的进程内响应缓存，覆盖 `/assets`(wall/summary)、`/stats`、`/notifications`、`/timeline`、`/overview`、`/friction` 总览（无筛选时）。所有 JSON 响应带 `ETag: "v<dataVersion>-<path hash>"`，`If-None-Match` 命中返回 304。
7. **首次导入进度。** `/api/v1/ingest/status` 扩展为：

```json
{ "status": "importing|ready", "data_version": 12,
  "import": { "phase": "assets|history|evaluate|idle", "files_seen": 1371, "files_read": 120, "files_skipped": 1251,
              "sessions_ingested": 118, "started_at": "...", "finished_at": null, "last_error": null },
  "assets": 153, "sessions": 874, "events": 418679 }
```

### 3.3 API 契约（A 实现，B 消费）

所有列表接口统一 `pagination: {offset, limit, total, has_more}`，统一 `data_version`。

**`GET /api/v1/sessions`**

参数：`q`（sessions_fts；`deep=1` 时同时命中 events_fts 并返回 `match_count` / `match_snippet`）、`project`（cwd 原值或 `__unrecorded__`，可重复）、`harness`（claude_code|codex）、`from`/`to`（`YYYY-MM-DD` 或 RFC3339，按 `started_at`）、`tag`（可重复，匹配任意 kind）、`has_friction=1`、`pinned=1`、`model`、`sort` ∈ `recent|oldest|duration|events|friction|tool_calls`、`limit`（默认 50，上限 200）、`offset`。

响应元素（顶层扁平，兼容现有详情页用法）：

```json
{ "id","source","source_session_id","title","task_text","started_at","ended_at","duration_ms",
  "harness_version","model","cwd","project_key","project_label",
  "event_count","transcript_count","message_count","user_message_count","tool_call_count","tool_result_count",
  "friction_count","tool_error_count","nonzero_exit_count","asset_count",
  "tags":[{"tag":"implementation","kind":"task"}], "pinned":false, "note_preview":null,
  "match_count":null, "match_snippet":null }
```

`project_key` = `cwd` 原值或 `__unrecorded__`；`project_label` = 路径最后一段或"项目未记录"。

**`GET /api/v1/sessions/facets`** — 与列表同参数（忽略 `sort/limit/offset`），返回在**当前其余筛选**下各维度的计数：

```json
{ "total": 874,
  "projects":[{"key":"/home/x/proj","label":"proj","cwd":"/home/x/proj","count":120}],
  "harnesses":[{"key":"codex","count":851}],
  "models":[{"key":"gpt-5","count":300}],
  "tags":[{"tag":"implementation","kind":"task","count":400}],
  "friction":{"with":210,"without":664},
  "pinned":3,
  "date_histogram":[{"day":"2026-08-01","count":12}] }
```

**`GET /api/v1/sessions/{id}`** — 现有响应不变，`session` 对象增加上面全部扁平统计字段，另加 `"tags"` 与 `"annotation": {"pinned","note","updated_at"}`。

**`PUT /api/v1/sessions/{id}/annotation`** — body `{"pinned"?:bool,"note"?:string,"tags"?:string[]}`；只更新给出的字段；`tags` 全量替换 `kind='user'` 标签；note ≤ 4000 字；返回 `{"annotation":…, "tags":[…]}`。只写本地 DB，bump `dataVersion`。

**`GET /api/v1/projects`**

```json
{ "projects":[{"key","label","cwd","sessions":120,"friction_count":38,"first_started_at","last_started_at",
               "harnesses":{"claude_code":20,"codex":100}}] }
```

**`GET /api/v1/overview?from=&to=`**（默认近 30 天；`from=all` 为全部）

```json
{ "range":{"from","to"}, "data_version":12,
  "sessions":{"in_range":210,"total":874,"by_harness":{"codex":200,"claude_code":10}},
  "projects":{"in_range":9,"total":14},
  "events":123456, "messages":9000, "tool_calls":40000,
  "duration":{"known_sessions":200,"total_ms":123456789},
  "friction":{"total":500,"tool_error":300,"nonzero_exit":150,"asset_violation":2,"user_interrupt":48,"sessions_with_friction":120},
  "activity_by_day":{"2026-08-01":{"sessions":4,"events":1200,"friction":6}},
  "top_projects":[…同 /projects 元素，附 in-range 计数…],
  "top_friction_tools":[{"tool_name":"Bash","count":220,"sessions":80}],
  "top_friction_categories":[{"category":"command_not_found","count":40}],
  "top_tags":[{"tag":"implementation","kind":"task","count":150}],
  "assets":{"total":153,"attention":0},
  "recent_sessions":[…8 条会话列表元素…],
  "last_event_at":"…" }
```

`top_friction_categories` 依赖 C 的 `friction_records.category` 列；A 在列不存在时返回空数组（不报错）。

> **A 实施补充（2026-08-22）——契约细化，B 按此实现**：
> - **同一维度内可重复参数取「或」**：`project` / `harness` / `model` / `tag` 重复出现时是多选（OR），维度之间是「与」（AND）。
> - **facets 每个维度排除它自己的筛选**：`harnesses` 在 `harness=codex` 生效时仍然给出两个 harness 的计数（其余筛选照常生效），这样多选面板不会自己把自己清空。`total` 与 `date_histogram` 之外的每个维度都遵此规则。
> - **`q` 短于 3 个字符时退化为 `LIKE`**，命中 `title/task_text/cwd/model`；trigram 分词器无法匹配少于 3 个字符，此时 `deep=1` 不生效，`match_count`/`match_snippet` 返回 `null`。
> - **`match_snippet` 从 `events.payload_json` 的正文里截取**，不用 FTS5 的 `snippet()`：`events_fts` 是 contentless 表（`content=''`），本身不存正文，`snippet()` 在它上面不可用。
> - **`/api/v1/ingest/status` 不带 ETag、永不进缓存**：它是导入进行中的轮询端点，内容会在 `data_version` 不变的情况下变化。
> - 会话列表元素额外带 `data_version`，详情响应也带；`pagination` 形如 `{offset,limit,total,has_more}`，`limit` 默认 50、上限 200。

## 4. 前端设计（B 负责）

### 4.1 加载模型

- 删除"每次路由先 `loadOverview()`"。每个页面只取自己需要的接口；壳（侧栏）只依赖 `/api/v1/ingest/status`（计数 + data_version + 导入进度）和 `/api/v1/projects`。
- 前端缓存以 `data_version` 为键：`get()` 带 `If-None-Match`，304 复用内存副本；切页不重拉，`data_version` 变了才重拉。每 20 s 轮询一次 `/ingest/status`（导入中时 3 s），版本变化后仅刷新当前页。
- 导入中在页面顶部显示一条细进度条 + "正在读取本地历史 120/1371 个文件"；不弹窗、不阻塞浏览。
- 所有列表渲染保留现有懒加载批次机制，并改为**服务端分页 + 滚动到底取下一页**。

### 4.2 会话页

```text
┌ 侧栏 ┐ ┌──────────────────────────────────────────────────────────────┐
│ 总览  │ │ 会话                        [874 个会话] [重新扫描]             │
│ 会话  │ │ [搜索：标题 / 任务 / 目录 / 模型  ○ 同时搜正文]                 │
│ 摩擦  │ │ [全部项目 ▾][全部 harness][时间：近7天/30天/90天/全部/自定义]   │
│ 资产  │ │ [标签 ▾][仅有摩擦][仅置顶]   分组：无/项目/日/周   排序 ▾       │
│ 时间线│ │ 活动条：按日柱状（facets.date_histogram，可拖选时间范围）       │
│ ───── │ │ ── 2026-08-22 · 12 个会话 ───────────────────────────────────  │
│ 项目  │ │ ▌[CC] 修复登录页闪烁   proj-a · 14:02 · 23m · 38 调用 · ⚠3 · 标签 │
│  proj-a│ │ ▌[CX] …                                                         │
│  proj-b│ │ …（滚动到底加载下一页）                                          │
└───────┘ └──────────────────────────────────────────────────────────────┘
```

- 筛选状态全部进 URL（`#/sessions?project=…&harness=…&from=…&tag=…&q=…&sort=…&group=…`），刷新不丢。
- 行内快捷操作：置顶（星标）、打标签（弹出小输入，回车添加，已有用户标签以 chip 展示，可删）、笔记指示（有笔记显示图标，hover 显示前 80 字）。
- 行的"⚠N"点击直达会话详情的摩擦轴。
- 空态与"未记录"按现有文案规则。

### 4.3 总览页

顶部时间范围（近 7/30/90 天/全部）。内容自上而下：

1. KPI 行：会话数 / 活跃项目 / 工具调用 / 摩擦事件 / 已记录时长（分子分母：`已记录时长的会话 200/210`）。
2. 活动热力图（由原统计页迁来，值改为会话数，hover 显示 会话/事件/摩擦）。
3. 项目表：项目 · harness 分布 · 会话 · 摩擦 · 最近活动 → 点击进会话页（带 project）。
4. 摩擦热点：按工具、按类别两列小表 → 点击进摩擦页（带筛选）。
5. 常见任务标签（规则标签计数）→ 点击进会话页（带 tag）。
6. 最近会话 8 条 + 资产关注数（silent/broken/bypassed 计数，点击进资产页）。

### 4.4 会话详情

头部增加：项目标签、harness、模型、置顶按钮、用户标签 chips、笔记（可编辑 textarea，失焦保存，保存后 toast "已记录笔记；源文件未改变"）。其余轨迹/事件流不动。

### 4.5 侧栏

`总览 / 会话 / 摩擦 / 资产 / 变化时间线`；"项目"分组列出 `/api/v1/projects` 前 8 个（按最近活动），末尾"全部项目 →"；"数据源"分组不变。计数徽标：会话总数、摩擦总数、资产关注数。

## 5. 摩擦分类与摩擦页（C 负责）

### 5.1 目标

把"工具出错 / 非零退出"这两个粗类细化成**一句话可解释的封闭类别**，并把"用户中断"作为一等摩擦记录下来；每一条都能回到会话里的那一个事件。

### 5.2 迁移 `007_friction_categories.sql`

`friction_records` 重建（SQLite 不能改 CHECK）：

- `friction_kind IN ('tool_error','user_interrupt')`；`event_type IN ('transcript_tool_result','transcript_message')`；
- 新增 `tool_name TEXT`（真实工具名，不是 `toolu_…`）、`category TEXT`、`category_rule TEXT`（命中的那条规则的一句话）、`classifier_version TEXT`；
- 索引 `(category)`, `(tool_name)`, `(session_id, friction_kind)`。

### 5.3 工具身份链接（C 在 `history/native.go` 只改这一处）

- Claude：`tool_use` 块补抓 `id`，写入 payload `tool_use_id`；`tool_result` 的 `tool_use_id` 已有。ingest 时 `tool_result` 通过同会话同 `tool_use_id` 的 `tool_call` 取真实 `tool_name`。
- Codex：`function_call` / `function_call_output` 都带 `call_id` 写入 payload；同法链接。
- 链接不到的显示"工具未记录"，不猜。

### 5.4 分类器 `internal/friction`

`Classify(kind, toolName string, payload map[string]any) (category, rule string)`。封闭类别（按优先级先命中先赢）：

| category | 规则（一句话） |
| --- | --- |
| `user_interrupt` | 消息文本等于/以 `[Request interrupted by user` 开头 |
| `permission_denied` | 输出含 `permission denied` / `EACCES` / `Operation not permitted` / `requires approval` |
| `command_not_found` | 输出含 `command not found` / `not recognized as an internal` / `No such file or directory` 且命令形态 |
| `file_not_found` | 输出含 `ENOENT` / `no such file` / `does not exist` / `File does not exist` |
| `tool_input_invalid` | 输出含 `InputValidationError` / `String to replace not found` / `must be absolute` / `old_string` |
| `timeout` | 输出含 `timed out` / `timeout` / `ETIMEDOUT` |
| `network_error` | 输出含 `ECONNREFUSED` / `ENOTFOUND` / `Could not resolve host` / `TLS handshake` / `502 Bad Gateway` / `503 Service Unavailable` / `504 Gateway Time`（实现时把裸 `TLS`/`502`/`503`/`504` 收窄：裸数字会命中 "line 504" 之类，且规则文案"输出包含 504"无法自圆其说；代价是部分网络错误退回 `nonzero_exit`） |
| `test_failure` | 输出含 `FAIL` 行 / `failed` 且工具是测试命令（`go test`/`pytest`/`npm test`/`jest`/`cargo test`） |
| `build_error` | 输出含 `error:` 且工具是编译/构建命令（`go build`/`tsc`/`cargo build`/`make`/`vet`） |
| `nonzero_exit` | 明确 `exit_code != 0` 且以上都未命中 |
| `tool_error` | 明确 `is_error=true` 且以上都未命中 |

已知误伤：`timeout` 规则的裸字面量会命中 `busy_timeout(5000)` 这类字符串，实测 55 条中未逐条核对；`user_interrupt` 目前只有 Claude Code 有明确记录，Codex 历史中未找到等价记录，不造规则。

规则只看**有界载荷**（已经存进 `friction_records.payload_json` 的内容），不回源文件；`classifier_version` 变化时 daemon 启动后整体重算（派生层可重算，ADR-10）。

### 5.5 `user_interrupt` 的采集

`eventstore.IngestFriction` 移到新文件 `internal/eventstore/friction.go`（C 拥有）；对 `transcript_message` 且文本命中中断模式的事件，写入 `friction_kind='user_interrupt'`。

### 5.6 API 与页面

- `/api/v1/friction` 总览增加 `category` 与 `tool` 筛选，总览表增加"按类别"与"按工具"两个可切换分组（项目×harness 仍是默认）；summary 增加 `user_interrupt_count` 与 `by_category`。
- 详情页事件列表每条显示：类别徽标（颜色+文字）、工具名、命中的规则一句话、会话标题、时间；点击"查看会话"进入会话详情并定位到该事件（`#/sessions/<id>?event=<event_id>`，B 负责详情页接受 `event=` 定位，C 负责跳转链接）。
- 把工作区现有未提交的摩擦页草稿（`app.js` / `style.css` 中 friction 段、`internal/api/friction.go`、`data_test.go` 新增用例）收口到上述形态。

## 6. 性能目标与验收

| 指标 | 目标 | 验证方法 |
| --- | --- | --- |
| daemon 启动到 `/healthz` 200 | < 1 s（冷启动，含迁移） | 脚本计时 |
| 重启后未变化文件重新解析数 | 0 | 日志 `files_skipped == files_seen` |
| `/api/v1/sessions?limit=50` | < 50 ms（874 会话） | `curl -w %{time_total}` |
| `/api/v1/sessions/facets` | < 100 ms | 同上 |
| `/api/v1/overview` | 首次 < 300 ms，缓存命中 < 5 ms | 同上 |
| `/api/v1/assets?view=wall` | 缓存命中 < 5 ms | 同上 |
| 页面切换（会话→总览→摩擦→资产） | 无全量重拉；Network 面板只见当前页接口 | 浏览器观察 |
| 后台 refresh 期间 API | 仍可响应（读不被写阻塞） | 刷新中 curl |

所有 Go 变更：`gofmt -l .` 为空、`go vet ./...`、`go test ./...` 全绿；新增表必须有 fixture 回放测试；派生表必须有"整体重算"入口。

> **A 实测（2026-08-22，977 MB 库 / 897 会话 / 36.0 万事件 / 931 资产 / 819 条摩擦，006+007 都已应用）**：
>
> | 项 | 实测 | 目标 |
> | --- | --- | --- |
> | exec → `/healthz` 200（热库重启） | **0.113 s** | < 1 s ✅ |
> | 首次建库（含 006 全量回填 FTS + session_stats） | 12.2 s，一次性 | —— |
> | 重启后未变化文件重新解析数 | `files_seen=1085 / files_skipped=1080`，只读了这段时间真的变过的 5 个文件 ✅ | 0 |
> | 历史导入耗时 | 首轮 3 分 45 秒 → 重启后 **9 秒** | —— |
> | `/sessions?limit=50` | 7–8 ms | < 50 ms ✅ |
> | `/sessions/facets` | 7–8 ms | < 100 ms ✅ |
> | `/overview` | 首次 15 ms / 缓存 2.2 ms | < 300 ms / < 5 ms ✅ |
> | `/projects` | 首次 3.6 ms / 缓存 2.0 ms | —— |
> | `/stats` | 首次 240 ms / 缓存 1.8 ms | —— |
> | `/friction`（总览） | 首次 69 ms / 缓存 2.2 ms | —— |
> | `If-None-Match` 命中（全部接口） | **1.7–2.2 ms / 0 字节** | —— |
> | 后台 refresh 期间 API | 200，`/sessions` 11 ms、`/facets` 6 ms、`/projects` 4 ms ✅ | 仍可响应 |
> | 资产评估 | 首轮全量 931 个约 5 s；之后每轮 `evaluated=0 skipped=931` 或只评估变化的那几个 | 原 2 分 13 秒 |
>
> 两条目标要按实测口径修正：`/assets?view=wall` 缓存命中 36 ms、`/timeline?limit=5000` 缓存命中 88 ms——这两项的耗时是 1.6 MB 与 9.1 MB 响应体的**传输**时间，不是查询时间，"缓存命中 < 5 ms"对这种体量的响应体不成立。真正让切页变快的是 304：这两个接口的 `If-None-Match` 命中同样是 2 ms / 0 字节。

## 7. 分工与文件归属（并行期间硬边界）

| 线 | 拥有文件 | 禁止触碰 |
| --- | --- | --- |
| A 数据层与性能 | `migrations/006_*.sql`、`internal/storage/*`、`internal/eventstore/store.go`（除 `IngestFriction`）、`internal/history/native.go`（除 §5.3 那几行）、`internal/runtime/*`、`internal/ingest/*`、`cmd/flatline/main.go`、新建 `internal/api/sessions.go` / `overview.go` / `projects.go` / `cache.go`、`internal/api/api.go`（仅：路由注册、删除被迁走的会话 handler、接入缓存/ETag） | `internal/api/friction.go`、`app.js`、`style.css` |
| B 前端 | `internal/web/static/app.js`（除 friction 段：`frictionQueryPath`…`drawFrictionDetail` 及其 action 分支）、`style.css`（除 `/* friction */` 段）、`index.html`、`internal/web/*_test.go` | 任何 Go 业务代码 |
| C 摩擦 | `migrations/007_*.sql`、新建 `internal/friction/*`、新建 `internal/eventstore/friction.go`（把 `IngestFriction` 搬过去）、`internal/api/friction.go`、`internal/history/native.go` 中 §5.3 的工具 id 捕获、`app.js`/`style.css` 的 friction 段、`data_test.go` 中 friction 用例 | 其它 |

共同规则：只用精确替换式编辑（不整文件覆盖 `app.js`/`style.css`/`api.go`）；编辑前重新读取目标区域；不 `git commit`；`storage.SchemaVersion` 由 A 设为 7（C 不改）；谁改契约谁先改本文。

---

# 第二阶段 · 会话层级、命令/文件投影、项目页、重复摩擦、时间统计、数据管理

> 2026-08-22 晚。依据：第一阶段上线后用本机真实数据（897 会话）的使用反馈。三条并行线：A2a 数据层与会话层级 / A2b 聚合 API 与摩擦签名 / B2 前端。

## 8. 使用反馈（真实数据）

| 发现 | 数据 | 含义 |
| --- | --- | --- |
| 列表被子代理线程淹没 | 873 个 Codex 会话里 **568 个**是 `thread_source=subagent`（`parent_thread_id`、`agent_role`=explore/architect/executor/…、`agent_nickname`） | 会话必须有父子层级；默认只看主会话 |
| Claude 子代理被并进父会话 | 188 个 `subagents/*.jsonl`，记录带 `isSidechain:true` 与 `agentId`，sessionId 同父会话 | 保持合并，但事件要标注 `agent_id`，统计要单列 |
| 空会话 | 203 个会话 0 条用户消息；64 个 0 次工具调用；123 个不足 1 分钟 | 默认隐藏空会话 |
| 命令可确定性抽取 | Claude `Bash.command`；Codex `exec_command`/`exec` 输入 3000/3000 含 `cmd:` 字段（JSON 或 JS 对象字面量） | 建 `session_commands` 投影：常用命令、失败命令 |
| 文件可确定性抽取 | Claude `Read/Edit/Write.file_path`；Codex `apply_patch` 的 `*** Update/Add/Delete File:` | 建 `session_files` 投影：会话改了哪些文件、项目热点文件 |
| 分类器误判 | `ls: cannot access … Exit code 2` 被判 `command_not_found` | `No such file or directory` 归 `file_not_found`；`command_not_found` 只认 "command not found"/"not recognized" 或 exit 127 |
| 没有项目视图 | 侧栏项目只是会话筛选 | 建项目页 |
| 重新扫描按钮是假的 | 只清前端缓存 | `POST /api/v1/ingest/refresh` |
| 时间线 9 MB | `timeline?limit=5000` | 首屏 `limit=1000` + 加载更多 |
| 工作时段 | `started_at` 小时分布明显 | 小时 × 星期热力图（按浏览器时区） |

## 9. 数据层（A2a）— 迁移 `008_session_hierarchy.sql`，`storage.SchemaVersion = 9`（A2b 的 009 见 §10）

```sql
ALTER TABLE sessions ADD COLUMN parent_session_id TEXT;   -- 'codex:<parent_thread_id>'
ALTER TABLE sessions ADD COLUMN thread_kind TEXT;         -- 'main' | 'subagent'；未知为 NULL（显示"未记录"）
ALTER TABLE sessions ADD COLUMN agent_role TEXT;
ALTER TABLE sessions ADD COLUMN agent_nickname TEXT;
ALTER TABLE sessions ADD COLUMN originator TEXT;          -- codex-tui / codex_exec / cli / claude_code
CREATE INDEX idx_sessions_parent ON sessions (parent_session_id);
CREATE INDEX idx_sessions_thread ON sessions (thread_kind, started_at);

ALTER TABLE session_stats ADD COLUMN subagent_count INTEGER NOT NULL DEFAULT 0; -- codex: 子会话数；claude: sidechain 事件的 distinct agent_id 数
ALTER TABLE session_stats ADD COLUMN command_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN failed_command_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN file_count INTEGER NOT NULL DEFAULT 0;     -- distinct path
ALTER TABLE session_stats ADD COLUMN is_empty INTEGER NOT NULL DEFAULT 0;       -- transcript_count=0 OR (user_message_count=0 AND tool_call_count=0)
ALTER TABLE session_stats ADD COLUMN projected_at TEXT;                          -- 投影跑过的时间戳；NULL = 从未投影（区分"投影过但没有命令"与"还没投影"）
CREATE INDEX idx_session_stats_empty ON session_stats (is_empty);                -- 列表默认按 is_empty 过滤

CREATE TABLE session_commands (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  event_id    INTEGER NOT NULL,              -- events.id（tool_call）
  tool_name   TEXT NOT NULL,
  program     TEXT,                          -- 第一个程序名（去掉 env 赋值、`cd x &&` 前缀、`sudo`、路径只留 basename）；解析不出为 NULL
  command     TEXT NOT NULL,                 -- 有界 512 字
  exit_code   INTEGER,                       -- 来自配对的 tool_result（同 call id）；未记录 NULL
  is_error    INTEGER,
  occurred_at TEXT,
  UNIQUE (session_id, event_id)
);
CREATE INDEX idx_session_commands_program ON session_commands (program, occurred_at);
CREATE INDEX idx_session_commands_session ON session_commands (session_id, id);

CREATE TABLE session_files (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  event_id    INTEGER NOT NULL,
  path        TEXT NOT NULL,                 -- 原值；相对路径按会话 cwd 补全为绝对路径（补不了保留原值）
  action      TEXT NOT NULL CHECK (action IN ('read','edit','write','delete')),
  tool_name   TEXT NOT NULL,
  occurred_at TEXT,
  UNIQUE (session_id, event_id, path, action)
);
CREATE INDEX idx_session_files_path ON session_files (path, occurred_at);
CREATE INDEX idx_session_files_session ON session_files (session_id, id);
```

抽取规则（一句话可解释，只看已落库的有界 payload，不回源文件）：

- 命令：Claude `tool_name=Bash` → `tool_input.command`；Codex `exec_command`/`exec` → JSON `cmd` 字段，或 JS 字面量 `cmd\s*:\s*("…"|'…'|`…`)`（取第一个；多条 `["label","cmd",cwd]` 数组形态逐条取第二个元素）。`write_stdin`/`wait` 不算命令。
- 文件：Claude `Read/Edit/Write/NotebookEdit` → `file_path`/`notebook_path`，action 分别 read/edit/write/edit；Codex `apply_patch` → 每个 `*** Update File:`=edit、`*** Add File:`=write、`*** Delete File:`=delete。
- 层级：Codex `session_meta.thread_source='subagent'` → `thread_kind='subagent'`，`parent_session_id='codex:'+parent_thread_id`，`agent_role/agent_nickname` 原值；否则 `thread_kind='main'`；`originator` 原值。Claude 顶层文件 `thread_kind='main'`，`originator='claude_code'`；`subagents/*.jsonl` 记录仍并入父会话，但事件 payload 加 `agent_id` 与 `sidechain:true`。
- 所有投影在每个会话 ingest 末尾重算该会话（先删后插），另有 `RecomputeAllProjections()` 供迁移后首启与 `flatline scan`。**迁移后首启必须对全部已有会话回填**（不依赖文件重放，因为指纹未变的文件不会重读）——从已入库的 events 重算即可。
- `session_stats` 的 `event_count/tool_call_count` 等保持含 sidechain 事件；`subagent_count` 单列。

> **A2a 实施补充（2026-08-22）——§9 规则细化，A2b/B2 按此实现**：
> - **`thread_kind` 缺失按"未记录"处理，不按 main 处理**。`thread=main`（默认）的条件是 `thread_kind IS NULL OR thread_kind='main'`：一个没有记录线程来源的会话并不"已知是子代理"，默认视图必须留下它，否则默认过滤会静默吞掉数据。`/sessions/facets` 的 `threads` 因此可能多出一个 `{"key":"__unrecorded__",count}` 项（计数为 0 时不出现），B2 需要按"未记录"渲染。
> - **`forked_from_id`**：`parent_thread_id` 缺失而 `forked_from_id` 非空时 `parent_session_id = 'codex:'+forked_from_id`；`thread_kind` 仍只由 `thread_source` 决定（缺省 main）。fork 线程因此是父会话的子线程但不是子代理。
> - **`subagent_count` 对 Codex = 全部子线程数**（子代理 + fork），与 `?parent=<id>` 返回的行数一致，这样 §11 的"⤷ N 子代理"展开数量与徽标数字不会对不上。Claude 的 sidechain 部分 = 事件 payload 里 distinct `agent_id` 与 `native_files` 里该会话 `subagents/agent-*.jsonl` 文件名所含 agent id 的并集——两者是同一批 agent 的两处显式记录，取并集是为了让**迁移前已入库、事件里没有 `agent_id`** 的历史会话也有真实计数（事件表 append-only，重放不会改写旧 payload）。
> - **`session_commands` 每个 tool_call 至多一行**（`UNIQUE (session_id, event_id)`）。因此 Codex `exec` 的 `const cmds = [["label","cmd",cwd], …]` 多命令形态只记录**第一条**；其余命令仍在 `events.payload_json` 原文里。
> - **patch 头按内容识别**：任何 tool_input 只要含 `*** Begin Patch` 就按 `*** Update/Add/Delete File:` 抽 `session_files`，不限于 `tool_name='apply_patch'`——Codex 的 `exec` 脚本里常内联调用 `tools.apply_patch`。
> - **`exit_code`/`is_error` 允许从已落库的 `tool_output` 文本再判一次**（不回源文件）：显式字段优先，缺失时按 `Exit code: N` / `Process exited with code N` / 开头 `Script failed` / `<tool_use_error>` 四条字面量规则判定。这四条与 `history.normalizeToolFailure` 共用 `canonical.NormalizeToolFailure`，解析期与投影期结论一致。
> - **配对键取 `tool_use_id` → `call_id` → `turn_id`**。旧解析器把 Codex 的 `call_id` 写进了 `turn_id`，事件是 append-only 的，重放不会改写，所以 `turn_id` 是最后兜底而不是同义词。**Claude Code 迁移前入库的 `transcript_tool_call` 事件既没有 `tool_use_id` 也没有 `call_id`**（旧解析器把 tool_use_id 错写进了 tool_result 的 `tool_name`），这些历史命令的 `exit_code`/`is_error` 只能是 NULL（未记录），直到对应文件发生变化被重读。
> - **`normalizeToolFailure` 新增两条规则**（对新解析生效）：输出匹配 `^.*\bProcess exited with code (\d+)` → `exit_code=N`；输出以 `Script failed` 开头 → `is_error=true`。同时 `Exit code: 0` 现在也会被记录为 `exit_code=0`（"成功"是事实，不是缺失）；`friction` 的判定仍是 `exit_code != 0`，不受影响。

## 9.1 会话 API 增量（A2a，`internal/api/sessions.go`）

`GET /api/v1/sessions` 新参数：`thread=main|subagent|all`（默认 `main`）、`empty=0|1|all`（默认 `0`=隐藏空会话）、`parent=<session id>`（列某会话的子会话，忽略 thread/empty 默认）、`program=<名>`、`file=<路径子串>`、`role=<agent_role>`。

列表元素新增：`thread_kind, parent_session_id, agent_role, agent_nickname, originator, subagent_count, command_count, failed_command_count, file_count, is_empty`。

`GET /api/v1/sessions/facets` 新增：`threads:[{key:"main",count},{key:"subagent",count}]`、`empty:{yes,no}`、`roles:[{key,count}]`、`programs:[{key,count}]`（前 30）。facets 的 `total` 与各维度都尊重 `thread`/`empty` 的默认值。

> **A2a 实施补充（2026-08-22）**：
> - `threads` 在存在未回填会话时多一项 `{"key":"__unrecorded__",count}`；`programs` 的 `count` 是**出现过该程序的会话数**（不是调用次数），因为这个面板驱动的是会话筛选。
> - **`roles` 这一个维度额外排除 `thread` 筛选**：`agent_role` 只存在于子代理线程上，若同时受默认 `thread=main` 约束，这个面板在默认视图里永远是空的。其余维度仍然只排除自己。
> - `?parent=<id>` 生效时忽略 `thread`/`empty` 的默认值（显式请求某个父会话的全部子线程）；`role`/`program`/`file` 与 `parent` 可以叠加。

`GET /api/v1/sessions/{id}` 新增：`parent:{id,title,project_label}|null`、`children:[列表元素…]`（≤100）、`commands:[{event_id,tool_name,program,command,exit_code,is_error,occurred_at}]`（≤500，附 `commands_total`）、`files:[{path,reads,edits,writes,deletes,first_event_id,last_at}]`（按 path 聚合，≤500，附 `files_total`）。

## 10. 聚合 API 与摩擦签名（A2b）

### 10.1 迁移 `009_friction_signature.sql`（A2b）

```sql
ALTER TABLE friction_records ADD COLUMN signature TEXT;   -- category||'|'||COALESCE(tool_name,'')||'|'||normalized_line
CREATE INDEX idx_friction_records_signature ON friction_records (signature);
```

`normalized_line`：取输出里第一行包含命中字面量的行（没有则第一行非空行），去掉前缀 `Exit code N`，小写，连续数字→`#`，绝对路径只留最后一段，截 120 字。签名随 `ReclassifyFriction` 一并重算；`ClassifierVersion` 升到 `friction/2`，同时修正 §8 的 `command_not_found`/`file_not_found` 规则。

### 10.2 端点

- `GET /api/v1/friction?group=signature` 新分组：`{signature, category, category_rule, tool_name, sample_line, count, session_count, project_count, first_occurred_at, last_occurred_at}`，排序默认 `session_count DESC, count DESC`；所有 friction 查询支持 `signature=` 筛选。summary 增加 `recurring_signatures`（session_count ≥ 2 的签名数）。
- `GET /api/v1/projects/{key}?from&to`（key 为 URL 编码的 cwd 或 `__unrecorded__`）：
  ```json
  { "project":{key,label,cwd,sessions,first_started_at,last_started_at,harnesses:{},originators:{}},
    "range":{from,to}, "data_version":n,
    "sessions":{"main":n,"subagent":n,"empty":n,"in_range":n},
    "duration":{"known_sessions":n,"total_ms":n},
    "by_week":[{"week":"2026-08-18","sessions":n,"duration_ms":n,"tool_calls":n,"friction":n}],
    "models":[{key,count}], "tags":[{tag,kind,count}], "roles":[{key,count}],
    "friction":{"total":n,"by_category":[{category,count,sessions}],"by_tool":[{tool_name,count,sessions}],"recurring":[签名分组元素 ×10]},
    "hot_files":[{path,sessions,reads,edits,writes,deletes,last_at}] (≤30, 按 edits+writes 再 sessions 排),
    "top_programs":[{program,calls,sessions,failures}] (≤20),
    "recent_sessions":[列表元素 ×8], "assets":{"total":n,"attention":n} }
  ```
- `GET /api/v1/stats/time?from&to&project&harness&tz_offset_minutes=` → `{"hour_weekday":[[24 个数]×7]（周一=0，按给定时区偏移换算）,"by_week":[{week,sessions,duration_ms,tool_calls,friction}],"by_day_of_week":[7],"tz_offset_minutes":n}`，只统计 `thread_kind='main'` 且非空会话。
- `GET /api/v1/tools?from&to&project&harness` → `{"tools":[{tool_name,harness,calls,sessions,failures}],"programs":[{program,calls,sessions,failures}]}`；`failures` 来自 `session_commands.exit_code!=0 OR is_error=1`，未记录不计。
- `POST /api/v1/ingest/refresh` → 正在导入返回 409 `{running:true}`；否则立即触发一轮 refresh，返回 202 `{started:true}`。
- `GET /api/v1/ingest/health` → `{schema_version, db_bytes, wal_bytes, last_import:{started_at,finished_at,files_seen,files_read,files_skipped,sessions_ingested,last_error}, warnings:[最近 20 条原文], unrecorded:{sessions_without_title,sessions_without_cwd,sessions_without_model,sessions_without_started_at,friction_without_tool}, counts:{sessions,main_sessions,subagent_sessions,empty_sessions,events,friction,commands,files,assets}}`；不缓存。
- `GET /api/v1/sessions/export?<列表同参数>&format=json|csv` → `Content-Disposition: attachment`，≤5000 行，字段同列表元素（csv 时 tags 以 `;` 连接，不含 task_text 全文以外的正文）。
- `GET /api/v1/search?q=&limit=` → `{"sessions":[≤10 列表元素],"projects":[≤5],"assets":[{id,name,kind,state}≤10],"programs":[≤5],"friction_categories":[{category,count}]}`，`q` 走 sessions_fts（<3 字符退化 LIKE）。
- `GET /api/v1/overview` 增加：`recurring_friction:[签名分组 ×5]`、`top_programs:[×8]`、`hot_files:[×8]`、`sessions.main/subagent/empty`。默认范围内只统计主会话且非空；`include=all` 时含全部。

> **A2b 实施补充（2026-08-22）——契约细化，B2 按此实现**：
>
> 1. **摩擦分组元素同时带 `friction_count` 与 `count`**（同值）。`friction_count` 是第一阶段既有字段，`count` 是 §10.2 的写法，两者都保留以免前端分叉。`sample_line` 就是签名的第三段（归一化后的证据行），不是原始行——这样不必为了展示再读一次 `payload_json`。
> 2. **签名取证据行时跳过 harness 包装行**。Codex 的 `exec_command` 会在真实输出前后加 `Chunk ID: …` / `Wall time…` / `Process exited with code N` / `Original token count:` / `Output:` / `Script failed` / `Script error:`。若不跳过，几千条不相关的失败会被 chunk id 归成一类。规则一句话：**签名取输出里第一行「带命中字面量」的行；没有字面量时取第一行不属于上述包装前缀的内容行**（整段都是包装时退回第一行，签名不会为空）。
> 3. **`command_not_found` 增加一条退出码规则**：输出含三条字面量之一，**或**明确记录 `exit_code=127`（shell 的「命令未找到」退出码）。退出码只在命令执行时存在，因此这条不再叠加 shell 工具判断。`No such file or directory` 一律归 `file_not_found`。
> 4. **`/tools` 的 `failures` 两条线不同源**：`programs` 来自 `session_commands`（`exit_code!=0 OR is_error=1`），`tools` 来自 `friction_records` 的同一判定——`session_commands` 只记录 shell 命令，用它给 `Read`/`Edit` 算失败会把「未记录」渲染成 0。`programs` 另加 `known_outcomes`（这些调用里有多少条真的记录了结果），前端应显示「N 次失败 / M 次已记录结果」，不要显示「0 次失败」。
> 5. **`/projects/{key}` 增加 `outside_project_files`**（该项目会话改过、但不在项目目录下的文件路径数，例如临时目录）；`hot_files` 只列项目目录下的文件。`/overview` 的 `hot_files` 排除 `/tmp/` 前缀，被排除的路径数放在 `scratch_files`。
> 6. **默认「主会话」= `thread_kind IS NULL OR thread_kind = 'main'`**，与会话列表 `thread=main` 完全一致：线程类型未记录的会话「不能确定是子代理」，不该被静默排除。`/overview` 增加 `scope:{main_sessions_only,excludes_empty,note}`，`include=all` 关闭该默认；`sessions.main/subagent/empty` 是**不受该默认影响**的分桶计数（`main` 含未记录，与筛选口径一致）。
> 7. **`/stats/time` 的 `tz_offset_minutes` 是「UTC 以东的分钟数」**（UTC+8 传 `480`），浏览器传 `-new Date().getTimezoneOffset()`。响应另带 `range` 与 `scope`。`by_week` 的 `week` 是该周周一的日期。
> 8. **`/ingest/health` 的 `counts`** 另有 `unrecorded_thread_sessions`（`thread_kind` 未记录的会话数，是 `main_sessions` 的子集）；`unrecorded` 另有 `friction_without_category`；`last_import` 另带 `phase`。008 未落地时 `commands`/`files`/`main_sessions`/`subagent_sessions`/`empty_sessions` 整键缺席（不是 0）。
> 9. **`/sessions/export` 的 JSON 体**为 `{sessions, exported, matched, truncated, data_version}`；CSV 首行是表头，`tags` 以 `;` 连接，未记录字段是空单元格。两种格式都不带 ETag、`Cache-Control: no-store`。
> 10. **`/search`** 响应另带 `q` 与 `data_version`；`programs` 元素为 `{program, calls, sessions}`；会话一段沿用会话列表的默认筛选（主会话、非空）。
> 11. **新增启动回填 `DeriveMissingFriction`**：旧解析器把 Codex 的结果写进了 `tool_output` 文本（`Process exited with code N` / `Script failed`）而没有 `exit_code`/`is_error` 字段，指纹未变的文件不会重读，这些事件只能从已入库的 events 补记摩擦。判定规则复用 `canonical.NormalizeToolFailure`，回填出来的 payload 带 `outcome_source:"tool_output"`，前端应据此标注「退出码读自输出文本」。该轮与 `ReclassifyFriction` 一起挪到**监听之后**执行（§3.2 第 1 条）。
> 12. **`/api/v1/ingest/status` 的 `import` 增加 `warnings`**（本轮导入的原文警告，最多 20 条），`/ingest/health` 直接透出同一份。

## 11. 前端（B2）

1. **会话页**：默认 `thread=main&empty=0`；工具栏两个开关"含子代理会话 (N)""含空会话 (N)"；主会话行若 `subagent_count>0` 显示"⤷ N 子代理"按钮，点开在行下内嵌展开子会话（`parent=`）；子代理行显示 `agent_role · agent_nickname` 徽标；新增"命令"下拉（facets.programs）与 `file=` 筛选芯片（从项目页热点文件进入）；分组新增"按角色"。
2. **会话详情**：头部显示父会话链接 / 角色；新标签页"命令与文件"：命令列表（程序名、命令、退出码徽标、点击定位事件、按程序筛选、只看失败）+ 文件列表（path、读/改/写/删计数、点击进入 `#/sessions?file=`）；子会话列表。
3. **项目页 `#/projects/<key>`**：§10.2 结构逐块渲染：头部（路径、harness/originator、时间范围）、周趋势条形（sessions/duration/friction 三指标切换）、模型/标签/角色分布、摩擦（类别、工具、反复出现）、热点文件（点击 → 会话页 `file=`）、常用命令（点击 → 会话页 `program=`）、最近会话。侧栏项目点击进项目页；项目页顶部"查看全部会话"。
4. **总览**：时间范围增加自定义起止日期；新增"工作时段"小时×星期热力图（`/stats/time`，传浏览器 `tz_offset_minutes`）、"反复出现的摩擦"前 5（签名、出现会话数、最近）、"常用命令"前 8、"热点文件"前 8；KPI 行注明"仅主会话、不含空会话"。
5. **摩擦页**：分组新增"反复出现"（signature），行显示样例行与"出现在 N 个会话"；详情支持 `signature=` 筛选；类别徽标继续复用。
6. **全局搜索 ⌘K**：侧栏搜索框改为全局：输入即查 `/api/v1/search`，下拉分组显示会话/项目/资产/命令/摩擦类别，回车进第一项，Esc 关闭；资产墙内的筛选保留为墙页自己的输入。
7. **数据页 `#/stats`** 改名"数据"：健康面板（`/ingest/health`：库大小、上次导入、文件读取/跳过、warnings 原文、未记录字段计数）、"立即重新扫描"按钮（POST refresh → 进度条复用导入进度）、"导出当前筛选的会话"（JSON/CSV，链接到 `/sessions/export?…` 带当前会话页筛选）、工具使用表（`/tools`）。会话页的"重新扫描"也改为真正触发。
8. **时间线**：首屏 `limit=1000`，底部"加载更多"。
9. 全部新文案中英双语；缺失显示"未记录"；不出总分。

## 12. 分工与文件归属（第二阶段）

| 线 | 拥有 | 禁止 |
| --- | --- | --- |
| A2a | `migrations/008_*.sql`、`internal/storage/*`（`SchemaVersion=9`）、`internal/history/native.go`、`internal/adapters/*`、`internal/eventstore/*`（**除** `friction.go`）、新建 `internal/eventstore/projections.go`、`internal/ingest/*`、`internal/runtime/app.go`、`internal/api/sessions.go`、`internal/api/sessions_test.go`、`testdata/` 会话 fixture | `internal/api/api.go`、`friction*`、`overview.go`、`projects.go`、`cmd/`、前端 |
| A2b | `migrations/009_*.sql`、`internal/friction/*`、`internal/eventstore/friction.go`、`internal/runtime/friction.go`、`internal/runtime/state.go`、`internal/runtime/nativefiles.go`、`cmd/flatline/main.go`、`internal/api/api.go`（路由、ingest status/health/refresh）、`internal/api/friction.go`、`projects.go`、`overview.go`、新建 `tools.go`/`search.go`/`export.go`/`timestats.go`、`internal/api/data_test.go`、新建 `internal/api/aggregate_test.go` | `sessions.go`、`native.go`、`adapters`、`eventstore/store.go`、`projections.go`、前端 |
| B2 | `internal/web/static/*`、`internal/web/*_test.go` | 任何 Go |

共同规则同 §7：精确替换式编辑、不整文件覆盖、编辑前重读、不 commit；`go build` 因他人文件失败就等再试；A2b 需要 `session_commands/session_files/sessions.parent_session_id` 等表列时按 §9 的定义直接写 SQL（A2a 负责落迁移），用 `PRAGMA table_info` 守卫缺列时返回空数组而不是 500。

---

# 第三阶段方向 · 摩擦生命周期（草案，依据 docs/qa/dogfood-2026-08-22.md）

**定义**：一个摩擦签名（category|tool|归一化行）在时间上的状态，只由已记录事件的时间决定：

| 状态 | 规则（一句话） | 谁看到什么 |
| --- | --- | --- |
| `new` | 首次出现在最近 7 天内 | 总览"新出现的摩擦"：值得看一眼 |
| `active` | 最近 7 天仍有记录，且首次出现早于 7 天前 | 总览"仍在发生"：反复撞上的东西 |
| `quiet` | 最近 7 天无记录，但历史上 ≥ 2 个会话出现过 | 摩擦页"已消失"：环境/规则改了之后它没再来 |
| `once` | 只出现过 1 个会话 | 默认折叠 |

误读纠正：`quiet` 不等于"已修复"——只是最近没发生；没有对应形状的会话时它本来就不会发生。所以 `quiet` 必须同时显示"最近 7 天同项目是否有会话"。

**API**：`group=signature` 元素增加 `status`、`sessions_last_7d`、`count_last_7d`、`days_active`（首末之间的天数）；`/overview` 增加 `friction_lifecycle:{new, active, quiet}` 计数与各前 5；`/projects/{key}.friction` 同。阈值 7 天可配置（`?window=14`）。

**前端**：总览"摩擦"区块改为三列：新出现 / 仍在发生 / 已消失；摩擦页"反复出现"分组默认按 `active` 排前，行首状态徽标（颜色+文字）。

---

# 13. A3 实施补充（2026-08-23）— 工具身份配对、签名 v3、工具投影、展示名、摩擦提示与生命周期

> 数据质量修复线。落 `migrations/010_event_pairs.sql`，`storage.SchemaVersion = 10`。
> 本节是契约的一部分：B2 按此消费新增字段。

## 13.1 迁移 `010_event_pairs.sql`

```sql
CREATE TABLE event_pairs (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  result_event_id INTEGER NOT NULL, call_event_id INTEGER NOT NULL,
  tool_name TEXT, pair_source TEXT NOT NULL CHECK (pair_source IN ('id','reparse')),
  PRIMARY KEY (session_id, result_event_id));
CREATE INDEX idx_event_pairs_call ON event_pairs (session_id, call_event_id);
ALTER TABLE native_files ADD COLUMN pairing_version TEXT;
CREATE TABLE tool_call_stats (session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  tool_name TEXT NOT NULL, harness TEXT NOT NULL, calls INTEGER NOT NULL,
  known_outcomes INTEGER NOT NULL, failures INTEGER NOT NULL,
  PRIMARY KEY (session_id, tool_name));
CREATE INDEX idx_tool_call_stats_tool ON tool_call_stats (tool_name);
CREATE INDEX idx_session_commands_program_outcome
  ON session_commands (program, session_id, exit_code, is_error);
```

## 13.2 配对机制（`event_pairs`）

**要回答的问题**：这一条 `transcript_tool_result` 是哪一次 `transcript_tool_call` 产生的？

| 情况 | 谁来配对 | 结果 |
| --- | --- | --- |
| 两边记了同一个 id（`tool_use_id` / `call_id` / `turn_id`） | 会话投影 `RecomputeSessionProjections`，在它本来就要解码每条工具 payload 的那一趟里用 Go 完成 | `pair_source='id'` |
| 两边 id 对不上（旧解析把 Codex 响应项自身 id 写进 `turn_id`：调用是 `fc_…`/`ctc_…`，结果是 `call_…`/`fco_…`/`ctco_…`） | daemon 启动后台的配对轮 `BackfillEventPairs` **只读重解析**一次原生 JSONL，按 `call_id` 配好，再用两边都已经带着的 `locator.raw_ref` 映射回 `events.id` | `pair_source='reparse'` |
| 源文件已不在磁盘 | 无 | 该会话保持未配对，`native_files.pairing_version` 不打戳，下次启动再试 |

**什么保证它**：`events` 表 append-only，配对轮只 `INSERT` 到 `event_pairs`，不 `UPDATE`/`DELETE` 任何 event（`internal/runtime/pairing_test.go` 用「事件行数 + 全表 id/payload/locator 摘要」在配对轮前后断言不变）。`native_files.pairing_version` 打戳后该文件永不再读。

**常见误读**：`pair_source='reparse'` **不是**「重放了源文件」——它没有产生任何事件，只是把源文件里已有的 `call_id` 关系记到旁边。

**为什么配对在 Go 里做而不是一条 SQL**：一条全库 `json_extract` 自连接在本机 36 万事件上跑了 5 分钟仍未结束（payload 列近 1 GB，每行要重新解析整段 JSON）；会话投影本来就要把每条 payload 解成 map，顺手取三个字段的成本是零。实测全库投影 902 个会话约 15 秒。

**下游谁读它**：`friction_records.tool_name`、`session_commands.exit_code/is_error`、`tool_call_stats.*` 全部从 `event_pairs` 取。配对轮结束后触发 `ReclassifyFriction`（重算 tool_name / category / signature / payload 里的 tool_name 与 tool_input）。

## 13.3 daemon 启动顺序与 `phase:"pairing"`

先监听，后配对（§3.2 第 1 条不变）。监听之后、首轮 refresh 之前跑配对轮，`/api/v1/ingest/status` 的 `import` 期间为：

```json
{ "phase": "pairing",
  "pairing": { "step": "projecting|reading|reclassifying", "files": 722, "files_read": 263, "pairs": 30523 } }
```

`import.pairing` 只在 `phase == "pairing"` 时存在（其余时候整键缺席）。三个 step 的含义：`projecting` = 重算全部会话的命令/文件/工具投影（同时写 id 配对）；`reading` = 只读重解析还有未配对结果的原生文件；`reclassifying` = 重算摩擦分类与签名。期间所有 API 正常应答（实测 11–20 ms）。

## 13.4 摩擦签名 v3（`ClassifierVersion = "friction/3"`）

`Signature(category, toolName, payload, program)` 新增第四个参数：配对到的那次调用跑的程序名，用 `session_commands` 同一个解析器抽出（`extractCommand` + `extractProgram`），没有则传 `""`。

样例行按以下顺序取，先命中先用：

1. 含「分类规则命中的那个字面量」的行；
2. 输出里有 `Traceback (most recent call last)` → 取最后一条非包装行（异常类型 + 消息，这才是能区分两个 traceback 的部分）；
3. 第一条命中 `(?i)error|fail|fatal|cannot|denied|not found|exception|panic|refused|blocked|rejected|unable to` 的内容行（跳过 harness 包装行）；
4. 都没有 → `<程序名|工具名> exit <N>`（只记了 `is_error` 时为 `<程序名|工具名> tool_error`；两者都没有时退回第一条内容行，签名永不为空）。

对 §10.2 原文的两处偏离，均已实测：
- 第 3 步的字面量表比原文多 `blocked|rejected|unable to`。否则 `Command blocked by PreToolUse hook: …`（20 个会话的反复摩擦）在配对补上 `tool_input` 之后会掉进第 4 步、退化成 `git exit …`，丢掉真正可区分的证据。
- 第 4 步的退出码**在归一化之后**拼接，因此 `pytest exit 1` 与 `pytest exit 5` 是两个签名（归一化会把数字变成 `#`）。

harness 包装行前缀表新增 `script completed` / `total output lines:` / `warning: truncated output`。归一化规则（去 `Exit code N` 前缀、小写、数字串→`#`、绝对路径只留最后一段、截 120 字）不变。

Codex 的 `turn_aborted` 记录签名为 `user_interrupt||interrupted`（取 `abort_reason` 原值）。

## 13.5 `user_interrupt` 覆盖 Codex

Codex 原生 JSONL 有明确的中断记录：`{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":…,"reason":"interrupted",…}}`。抽样 150 个 rollout 文件共 74 条，`reason` 全部是 `interrupted`。

规则一句话：**Codex 记录了 `turn_aborted` 且 `reason="interrupted"`**。落地方式：`readCodex` 把这条记录写成一条 `transcript_message` 事件，payload 只带 `abort_reason`（不伪造任何消息正文）；`frictionKindOf` 见到 `abort_reason=="interrupted"` 记为 `friction_kind='user_interrupt'`。

`session_stats.message_count` 相应排除 `abort_reason IS NOT NULL` 的 `transcript_message`：中断记录是一条转写记录，但它不是一条消息。

## 13.6 工具统计投影 `tool_call_stats`

每个会话 ingest 末尾按会话重算（先删后插，与 `session_commands`/`session_files` 同一事务）。一行 = 一个会话里的一个工具：

- `calls`：该工具的 `transcript_tool_call` 事件数；源里没写工具名的记在 `__unrecorded__`。
- `known_outcomes`：这些调用里，配对到的结果**真的记了** `exit_code` 或 `is_error` 的条数。
- `failures`：从 `known_outcomes` 里数出来的失败（`is_error=1` 或 `exit_code!=0`）。

`/api/v1/tools` 的 `tools` 数组改读该投影，元素新增 `known_outcomes`。**前端必须显示「N 次失败 / M 次已记录结果」，不要显示「0 次失败」**——`calls - known_outcomes` 是未记录，不是成功。`programs` 仍读 `session_commands`（§10.2 不变）。

## 13.7 会话展示名 `display_title` / `title_source`

会话列表、会话详情、`/search` 的会话元素、`/sessions/export` 全部新增两个字段：

| `title_source` | `display_title` 取自 |
| --- | --- |
| `ai` | `title`（harness 自己写的标题） |
| `task` | 没有 title，用 `task_text`（截 120 字） |
| `synthesized` | 两者都没有、但记了子代理身份：`"<agent_role> · <agent_nickname> ← <父会话展示名前 60 字>"` |
| `none` | 什么都没记 → `display_title` 为 `null`，前端显示「未记录」 |

父会话展示名只回溯一层（父会话的 `title` → `task_text`）；父会话也没有时合成名只剩 `<role> · <nickname>`。

## 13.8 摩擦提示字典 `internal/friction/hints.go`

封闭的正则表，按顺序先命中先用，匹配对象是整条签名（`category|tool|样例行`）。`kind` 封闭集合：`environment` / `harness_rule` / `user_hook` / `tool_misuse` / `permission` / `timeout` / `test` / `build`。`mechanism` 只说这是什么机制，**不写建议、不写因果**。

- `group=signature` 元素、`/overview.recurring_friction`、`/projects/{key}.friction.recurring` 每个元素新增 `hint: {kind, mechanism} | null`。`null` = 字典里没有覆盖这条，不是「没有机制」。
- 摩擦 summary 与 `/projects/{key}.friction` 新增 `by_hint_kind: [{kind, signatures, count}]`，按 count 降序；字典没覆盖的签名归 `__unrecorded__`。没有签名的记录（未分类）不进这个分布。

## 13.9 摩擦生命周期（signature 分组）

窗口默认 7 天，`?window=<天数>`（1–365）可调；响应带 `window_days`。

| `status` | 规则（一句话） |
| --- | --- |
| `new` | 首次记录落在窗口内 |
| `active` | 窗口内还有记录，且首次记录早于窗口 |
| `quiet` | 窗口内没有记录，但历史上出现在 ≥ 2 个会话 |
| `once` | 只出现过 1 个会话 |

四条按此顺序判定，先命中先用，因此覆盖完全、互斥。

signature 分组元素新增：`status`、`sessions_last_7d`、`count_last_7d`、`days_active`（首末记录之间的整天数）、以及**只有 `quiet` 元素才有**的 `project_sessions_last_7d`（该签名出现过的那些项目，窗口内一共跑了多少个会话）。字段名固定带 `7d`，实际窗口以 `window_days` 为准。

**误读纠正**：`quiet` 不等于「已修复」。它只说明窗口内没再记录到；同一批项目窗口内本来就没跑会话时，它也会是 `quiet`。`project_sessions_last_7d` 就是用来区分这两种情况的——前端展示 `quiet` 时必须一并展示它。

排序：`group=signature` 且 `sort=sessions`（该分组的默认）时，顺序为 `active` 优先 → `sessions_last_7d` 降序 → `session_count` 降序 → `friction_count` 降序 → 最近时间降序。`sort=count` / `sort=recent` 维持原语义。

`/overview` 新增 `friction_lifecycle: {window_days, new, active, quiet, once, top_new[×5], top_active[×5], top_quiet[×5]}`；`/projects/{key}.friction.lifecycle` 同形。计数覆盖筛选范围内的全部签名（扫描上限 5000 个签名），三个 top 列表各取前 5。

## 13.10 项目 `is_home_dir`

`/projects` 元素与 `/projects/{key}.project` 新增 `is_home_dir`：cwd 等于 `$HOME`，或是 `$HOME` 的直接一级子目录且该目录下没有 `.git`。**只做标记，不改分组、不改计数**——前端可以据此把「从家目录发起」的会话与真正的项目区分开。

---

# 第四阶段 · 会话管理线：准确的会话度量与多源覆盖（A4 契约草案）

> 依据用户 2026-08-23 定调：统计必须准确；会话管理（token、轮次、改动量、时长，跨 harness/模型/项目/环境）本身就是价值；不堆功能。

## 14. 原始数据里已核实的度量来源

| 度量 | Claude Code | Codex | 准确性要点 |
| --- | --- | --- | --- |
| token | 每条 assistant 记录的 `message.usage`：`input_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`、`output_tokens`、`output_tokens_details.thinking_tokens` | `event_msg type=token_count`：`info.total_token_usage`（累计）与 `info.last_token_usage`（本轮），字段 `input_tokens/cached_input_tokens/cache_write_input_tokens/output_tokens/reasoning_output_tokens/total_tokens`，另有 `model_context_window` | **Claude 同一 `message.id` 会拆成多条记录（thinking / tool_use 各一条），前几条的 usage 是占位值（output_tokens 个位数），只有最后一条（带 `stop_reason`）才是真实用量：去重必须取同 id 的最后一条（= max(output_tokens)），取第一条会少算约 3–4 倍（实测 16,091 vs 58,304）**；Codex 取最后一条 `total_token_usage` 为会话总量，逐轮用 `last_token_usage` |
| 轮次 | assistant 记录 `stop_reason=end_turn` 的条数 = 助手回合；`role=user` 非 sidechain 消息数 = 用户回合 | `turn_context` 条数 = 回合；`message role=user` = 用户回合 | 子代理 sidechain 不计入父会话回合 |
| 模型 | 每条 assistant 记录 `message.model`（会话内可能多个，如 `claude-fable-5` 与 `opus`） | 每个 `turn_context.model` | `sessions.model` 只保留首个；新增按模型分桶的 token |
| 改动量 | `Edit`：`old_string`/`new_string` 行数差；`Write`：`content` 行数；`NotebookEdit` | `apply_patch`：`+`/`-` 行计数（按 `*** Update/Add/Delete File` 分文件） | **已入库 payload 有界（8192 字）：1047/158,580 个 tool_call 被截断（apply_patch 251/2722、Write 232/757），所以改动量必须在解析期对完整输入计算**，不能从有界 payload 推 |
| 时长 | 首末 timestamp | 同 | 已有；另加"活跃时长"=相邻事件间隔 ≤ 10 分钟的累计（空闲不计） |

## 15. 设计：从原文派生的、版本化的会话度量

原则：events 表仍 append-only；度量是派生投影，带 `derive_version`，版本变化时**重读原文件一次**重算（与 A3 的 `pairing_version` 合并为 `native_files.derive_version`），期间 API 不阻塞、进度在 `ingest/status.import.phase="derive"`。

```sql
CREATE TABLE session_usage (
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  input_tokens INTEGER, cached_input_tokens INTEGER, cache_write_tokens INTEGER,
  output_tokens INTEGER, reasoning_tokens INTEGER, total_tokens INTEGER,   -- 未记录为 NULL，不是 0
  assistant_turns INTEGER, user_turns INTEGER,
  lines_added INTEGER, lines_removed INTEGER, files_changed INTEGER,
  active_ms INTEGER,                                                      -- 空闲 >10min 不计
  context_window INTEGER, usage_source TEXT NOT NULL,                     -- 'claude_usage' | 'codex_token_count' | 'unrecorded'
  derive_version TEXT NOT NULL, computed_at TEXT NOT NULL
);
CREATE TABLE session_model_usage (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  model TEXT NOT NULL, turns INTEGER NOT NULL, input_tokens INTEGER, output_tokens INTEGER, total_tokens INTEGER,
  PRIMARY KEY (session_id, model)
);
```

API：会话列表/详情/项目页/总览加 `usage` 对象（字段同上，NULL → "未记录"），排序增加 `tokens`、`lines_changed`、`active`；`/overview` 与 `/projects/{key}` 加 `usage:{known_sessions, total_tokens, output_tokens, lines_added, lines_removed, active_ms}`（分子分母：`known_sessions/in_range`）与 `by_model:[{model, sessions, total_tokens}]`。

## 16. 准确性门禁（每个度量端点必须过）

1. **一致性测试**：同一口径的数在所有端点相等——`health.counts.sessions == overview.sessions.total == facets(thread=all,empty=all).total == stats.session_count == Σprojects.sessions`；`health.friction == friction.summary.total_events`；列表的 `pagination.total` 与 facets 的 `total` 相等。写成一个表驱动测试，任何端点改口径就红。
2. **去重测试**：Claude 同一 message.id 多条记录只计一次 usage；Codex 累计量取最后一条而非求和。
3. **截断测试**：改动量对超过 8192 字的 Write/apply_patch 仍准确（fixture 含一个 20 KB 的输入）。
4. **缺失≠零**：没有 usage 记录的会话（例如旧版 Claude 文件无 usage）`usage_source='unrecorded'`，所有 token 字段 NULL；UI 显示"未记录"，聚合时只进分母说明。
5. **原文核对**：`scripts/audit_accuracy.py` 随机抽样，用原生转录独立重数用户轮 / 工具调用 / 工具结果 / token，与 API 逐项相等（harness 注入的上下文消息如 `<environment_context>`、`<recommended_plugins>`、`<turn_aborted>` 不算用户轮）。每次构建后跑，报告贴输出。
6. **跨 harness 同口径**：子代理转录一律是独立子会话（`thread_kind=subagent` + `parent_session_id`），父会话只统计主线；任何"每会话 X"的数在 Claude Code 与 Codex 下含义必须相同。

## 17. 多源覆盖（会话管理线，草案，排在 A4 之后）

- `sources` 配置表：`{id, kind: claude_code|codex, root, label, machine_label, enabled}`；daemon 启动读取，UI 数据页可增删（只读扫描，不写源目录）；每个会话记 `source_id` → 展示"来自哪台机器/哪个目录"。
- 同步过来的目录（例如别的机器 rsync 来的 `~/.codex/sessions`）只需加一个 source；指纹表按绝对路径区分。

---

# 第五阶段 · 会话覆盖：更多 harness 适配器（A5 契约草案）

> 用户 2026-08-23："对接各种主流 harness（dsh、opencode、Hermes、pi 等）肯定要做，安排一条 agent 线逐步实现。"

## 18. 本机已核实的数据源

| harness | 位置 | 形态 | 规模 | 可直接取到的度量 |
| --- | --- | --- | --- | --- |
| opencode | `~/.local/share/opencode/opencode.db`（SQLite，WAL，运行中会写） | `session`（id、project_id、parent_id、directory、title、version、agent、model JSON、tokens_input/output/reasoning/cache_read/cache_write、cost、summary_additions/deletions/files、time_created/updated/archived）、`message`（data JSON：role、time、agent、model）、`part`（data JSON：type=text/tool/…）、`project`（worktree、name）、`todo` | 51 会话 / 13,182 消息 / 53,455 part | token、成本、改动量、父子会话（`parent_id`）、项目目录 |
| dsh | `~/.dsh/sessions/<project-slug>/session-<uuid>/session.jsonl.zstd` | **zstd 压缩** JSONL | 14 会话 | 待解压后核对 |
| Hermes | `~/.hermes/sessions/`（目前为空）、`~/.hermes/logs`、`skills`、`hooks`、`memories` | 未知 | 0 会话 | 先做探测与"未记录"，有数据后再做 |
| pi / Gemini CLI / Aider / Cursor 等 | 本机未安装 | — | — | 列入适配器清单，按格式文档实现时需 fixture |

## 19. 适配器框架要求

1. **来源是开放枚举**：`sessions.source` 的 CHECK（`IN ('claude_code','codex')`）通过迁移重建为无 CHECK（沿用 003 的 `_vN` 重建模式，保留所有行与索引）；Go 侧 `adapters.Source` 改为注册表驱动，新增来源只需注册，不改 canonical 校验。
2. **每个适配器 = 一个 reader + 一个 adapter**：reader（`internal/history/<name>.go`）把原生存储读成与现有 `marshalClaude/marshalCodex` 同形的 normalized JSON（session 元数据 + messages：message / tool_call / tool_result，含 `call_id` 配对键、`is_error/exit_code`、token usage、层级字段）；adapter（`internal/adapters/<name>/`）把 normalized JSON 变成 canonical events。只读；SQLite 源用 `mode=ro` + `busy_timeout` 打开，绝不写。
3. **指纹**：文件型源沿用 `native_files`（size/mtime）；SQLite 型源以 `session.time_updated` 为指纹（同表一行一会话），记录在 `native_files` 里用伪路径 `opencode.db#<session_id>`。
4. **压缩**：dsh 需要 zstd 解码；引入纯 Go 的 `github.com/klauspost/compress/zstd`（无 CGO、无网络），并写一条 ADR-19 说明"适配器允许引入纯 Go 的格式解码依赖"。
5. **度量对齐**：opencode 的 token/成本/改动量直接进 `session_usage`（§15，`usage_source='opencode_session'`），不要再从 part 推算；其它源按 §14 规则。
6. **每个适配器的验收**：合成 fixture 回放测试（不放真实数据）；真实本机数据导入后报告：会话数、事件数、层级数、usage 已记录数、摩擦数、未记录字段清单；`/ingest/health` 新增 `sources:[{kind, root, sessions, last_seen_at, status}]`。
7. **daemon 配置**：`-opencode-db`、`-dsh-root`、`-hermes-root` 三个 flag，默认按本机常见位置探测；探测不到时在 health 里显示 `status:"not_found"`，不是错误。

## 21. A5 实施补充（实测后对 §18–§19 的修订）

本节记录 A5 在本机实测三个数据源后，对 §18/§19 的具体落地方式与两处**契约修订**。
凡与 §18/§19 冲突的，以本节为准。

### 21.1 修订一：迁移 012 不重建 `sessions`，改用 `writable_schema` 摘除 CHECK

§19.1 要求"沿用 003 的 `_vN` 重建模式"。**这条在本库行不通，实测会删光数据**，理由如下。

- `storage.Open` 用 `_pragma=foreign_keys(1)` 打开连接，外键强制**始终开着**；
  `events`、`session_stats`、`session_tags`、`session_annotations`、`friction_records`、
  `event_pairs`、`session_commands`、`session_files` 全部 `REFERENCES sessions(id) ON DELETE CASCADE`。
- SQLite 的 `DROP TABLE` 会执行一次隐式 `DELETE FROM`，**会触发 ON DELETE CASCADE**。
- 官方重建流程第一步是 `PRAGMA foreign_keys=OFF`，而该 pragma 在事务内是空操作；
  `storage.applyOne` 把每个迁移都包在事务里，且 `storage.go` 不在 A5 的可改文件内。
- 实测：`_vN` 重建 + `PRAGMA legacy_alter_table=ON` 在事务内**不生效**，
  `ALTER TABLE sessions RENAME` 仍改写了子表的 REFERENCES，
  随后的 `DROP TABLE sessions_v11` 把 events / session_stats / session_tags /
  session_annotations 全部级联删空（1 → 0）。

**改用的做法**（SQLite 官方为"不改变磁盘表示的 schema 变更"所许可的 `writable_schema` 路径）：

```sql
PRAGMA writable_schema = ON;
UPDATE sqlite_master
SET sql = replace(sql, ' CHECK (source IN (''claude_code'', ''codex''))', '')
WHERE type = 'table' AND name = 'sessions';
PRAGMA writable_schema = RESET;
```

摘除 CHECK 不改变行的磁盘格式，所以**不搬一行数据**：行、索引、外键、级联全部原样保留。
`RESET` 让当前连接重新装载 schema（只写 `OFF` 会让本连接继续用缓存里的旧 CHECK——实测如此）。

这个做法还有一个 §19 的方案没有的好处：它**与列集无关**。A4 的迁移 011 若给 `sessions`
加列，012 不需要跟着改；而 `_vN` 重建必须把新列写进 012 的建表语句，否则静默丢列。

**四要素**：

1. **迁移 012 回答的问题**：`sessions.source` 的取值是否还被 CHECK 限死在两个 harness 上？
2. **结果与谁承接**：

   | 结果 | 谁承接 |
   | --- | --- |
   | CHECK 已摘除 | 迁移记入 `schema_migrations`，新来源可以写 `sessions` |
   | `replace()` 没匹配上（DDL 文本被改过） | 守卫表 CHECK 失败 → 整个迁移事务回滚，daemon 启动失败并报错，不会留下"以为开了其实没开"的中间态 |

3. **靠什么强制**：迁移末尾的守卫表——
   `CREATE TABLE migration_012_guard (ok INTEGER NOT NULL CHECK (ok = 1))`，
   插入 `instr(sql,'CHECK (source IN') = 0` 的判定值。没摘干净就插 0，CHECK 失败，事务回滚。
   另有 `migrations/migrations_replay_test.go` 回放 001→012 并断言子表行数不变。
4. **容易误读的地方**：*"改了 `sqlite_master` 就是改了数据"*——不是。摘除 CHECK 只改
   schema 的文本表示，行的编码格式完全不变，这正是 SQLite 允许 `writable_schema` 的场景；
   任何会改变磁盘表示的变更（加列、改类型）都**不能**这样做，必须走重建。

**回滚说明**：把 CHECK 加回去即可（`writable_schema` 反向 `replace`），但加回去之前必须先
`DELETE FROM sessions WHERE source NOT IN ('claude_code','codex')`——那会级联删掉这些
会话的全部事件。所以回滚等于放弃新来源的数据，不是无损操作。

### 21.2 修订二：`adapters.Source` 的校验口径

`Source.Valid()` 原先硬编码两个常量。改为：**一个来源合法，当且仅当它已在
`adapters` 的来源注册表里登记过**。`Register(adapter)` 在登记适配器时自动登记它的来源；
`RegisterSource` 供 reader 侧（还没有 adapter 的探测型来源）单独登记。
`internal/canonical` 不含来源枚举（`Locator.Source` 只校验非空），所以无需改动。

`storage.SchemaVersion` 由 A4 置为 12，A5 不动。

### 21.3 三个来源的实测形状（细节见 `docs/field-matrix-opencode.md` / `docs/field-matrix-dsh.md`）

| 来源 | 标识串 | 展示名 | 实测规模 | 指纹 |
| --- | --- | --- | --- | --- |
| opencode | `opencode` | opencode | 51 会话 / 13,182 message / 53,455 part，18 个会话有 `parent_id` | 伪路径 `<db>#<session_id>` + `session.time_updated` |
| dsh | `dsh` | DeepSeek Harness | 14 会话（zstd JSONL），9,826 条记录 | 真实文件路径 + size/mtime |
| Hermes | `hermes` | Hermes | 0 会话（`~/.hermes/sessions` 为空） | 只探测，不解析 |

- **opencode 配对键**：`part.data.callID`，tool_call / tool_result 都写进 normalized JSON 的
  `call_id`（§13 要求）。失败用 `state.status='error'` → `is_error=true`，
  `state.metadata.exit` → `exit_code`；两者都缺则为 null（未记录），不补 0。
- **dsh 配对键**：`tool/call.data.callId` 与 `tool/result.data.message.source.callId`，同样落 `call_id`。
  失败用 `content[].isError` 与 `data.error`；dsh **不记录 exit code**，`exit_code` 恒为 null。
- **usage**：opencode 用 `session` 行的 `tokens_*`/`cost`/`summary_*`，`usage_source='opencode_session'`；
  dsh 用 `assistant/message.data.usage` 逐条累加，`usage_source='dsh_message_usage'`。
  两者都按 §14 的字段名放进 normalized JSON 的 `session.usage`，由 A4 的派生管线统一写入 `session_usage`。

### 21.4 层级

- opencode：`session.parent_id` → `parent_session_id = "opencode:" + parent_id`，
  `thread_kind` 相应为 `subagent`；无 parent 则 `main`。`agent` 列 → `agent_role`。
- dsh：会话头有 `delegationDepth`，本机 14 个会话全是 0，**没有 parent id 字段**。
  因此 dsh 只在 `delegationDepth = 0` 时记 `thread_kind='main'`；深度大于 0 时记
  `thread_kind='subagent'` 且 `parent_session_id` 留空（未记录），不从目录名或 id 猜父会话。

---

# 20. A4 实施补充（2026-08-23）— 退出码语义、版本化重读、会话度量、子代理归属

> 准确性线。落 `migrations/011_session_usage.sql`，`storage.SchemaVersion = 12`（A5 的 012 已就位）。
> 本节是契约的一部分：B 线按此消费新增字段。

## 20.1 退出码语义表（`internal/friction/exitcodes.go`）

**要回答的问题**：这条被记录下来的非零退出码，是"失败"还是"程序给出的答案"？

`rg` 退出码 1 = 没有匹配到内容，不是失败。它在本机是第一大"摩擦"签名（137 个会话）。把它当摩擦，是错误数据。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| 分类器 `friction.Classify` | 命中字面量规则之后、落到 `nonzero_exit` 之前，查一次封闭的 `{program, exit_code}` 表 | `is_failure=false` → `category='expected_exit'`；`is_failure=true` 且码是 124 → `category='timeout'`；其余照旧 |
| 命令/工具投影 `projectToolEvents` | 对每次工具调用的完整命令行查同一张表 | `session_commands.expected_exit=1`；`tool_call_stats.failures` 不计它 |
| 会话投影 `session_stats` | `friction_count` / `tool_error_count` / `nonzero_exit_count` 排除 `category='expected_exit'` | 另出 `expected_exit_count` |
| 摩擦 API | 默认过滤掉这些记录 | summary 出 `expected_exit_count`，前端显示"已排除 N 条预期非零退出" |

**表里有什么**（每条都是程序自己文档化的约定，不是推断）：

| 程序 | 码 | 含义 | 是否失败 |
| --- | --- | --- | --- |
| `rg` `ripgrep` `grep` `egrep` `fgrep` `ag` `ack` | 1 | 没有匹配到内容 | 否 |
| `diff` `cmp` `colordiff` | 1 | 两边有差异 | 否 |
| `pkill` `pgrep` | 1 | 没有匹配到进程 | 否 |
| `test` `[` | 1 | 条件为假 | 否 |
| `pytest` `py.test` | 5 | 没有收集到测试用例 | 否 |
| 任意 | 124 | `timeout` 超时中止（归入 `timeout` 类） | 是 |
| 任意 | 130 / 137 / 143 | SIGINT / SIGKILL / SIGTERM 中止 | 是 |

**按哪个程序查表**：shell 报告的是**最后一条语句**的退出状态。`ExitCandidates` 因此取最后一条语句，再把它按 `&&`/`||` 拆成若干候选（任一条都可能是中断链条的那条），每条候选取其管道的最后一个程序。`cd`/`export`/`source`/`true`/`:` 这些不会拥有非零状态的前缀被剔除；`sudo`/`time`/`nohup`/`timeout <时长>`/`env` 被跨过；`python -m pytest`、`poetry run pytest` 解到被运行的那个程序。**只有全部候选对这个码都给出同一条 `is_failure=false` 的条目时才套用**——任一候选没有条目就不套用，宁可少判，不会把真失败说成"预期"。

**什么保证它**：`internal/friction/exitcodes_test.go` 表驱动（24 条），覆盖管道、`&&` 链、`cd` 前缀、`timeout` 包裹、`-m`/`run` 解包与"候选不一致就不套用"。

**常见误读**：`expected_exit` **不是**"这条记录被删掉了"。记录照常存在 `friction_records` 里，只是不算作摩擦；`category=expected_exit` 仍可显式筛出来。

## 20.2 签名样例行跳过装饰行（`ClassifierVersion = "friction/4"`）

§13.4 的第 1 步补一条：命中字面量的那一行如果是**装饰行**，它不能作为样例。装饰行 = 整行只由 `= - _ * ~ # ! +` 与空格组成，或者两端各有 ≥3 个同一装饰字符（`=== FAILURES ===`、`------ Captured stderr ------`、`____ TestX ____`、`!!!! stopping !!!!`）。这些行在每次运行里一模一样，签在上面会把互不相关的失败并成一个签名。

规则一句话：**关键字落在装饰行上时，先在其后找同样含该关键字的非装饰行，找不到再取其后第一条非装饰内容行。** pytest 因此签在 `FAILED tests/x.py::test_y - AssertionError…` 上，而不是 `=== FAILURES ===` 上。第 2、3 步与第 4 步的兜底同样跳过装饰行。

## 20.3 `parser_version` 统一重读

`native_files.parser_version` 记录哪一版解析器读过这个文件（迁移 011）。`pairing_version`（迁移 010）保留在原处，回滚到那一版 daemon 时它仍能找到自己的戳。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| daemon 启动（监听之后、首轮 refresh 之前，`BackfillEventPairs` 的第 0 步） | 列出 `parser_version` 与 `history.ParserVersion` 不符的文件，逐个重读，**走正常 `pipeline.Ingest`** | events 幂等插入，只新增旧解析器漏掉的记录（Codex `turn_aborted`、`token_count`）；已存的事件一行不改 |
| 同上 | 每读完一个文件打戳 | 打过戳的文件永不再读 |
| 进度 | `ingest/status.import.phase="reparse"`，`import.reparse = {files, files_read, events_inserted}` | 期间所有 API 正常应答 |

来源不是"能按路径重读的转写文件"（opencode 的 SQLite 行、Hermes）时跳过且不打戳。

## 20.4 会话度量 `session_usage` / `session_model_usage`

度量只能从**原文全文**取：入库 payload 有界（8192 字），1047 个 tool_call 被截断。因此度量在解析期算，写在 ingest 末尾。

**为什么多一张 `native_file_usage`**：一个 Claude Code 会话是「主转写 + 每个子代理一个文件」。直接把某个文件的度量写进会话行，最后读到的那个子代理文件会覆盖整会话的 token。`native_file_usage` 记每个文件读到了什么，`session_usage` / `session_model_usage` 是这些文件的汇总。

| 度量 | 取自 | 准确性要点 |
| --- | --- | --- |
| token | Claude：每条 assistant 记录的 `message.usage`；Codex：`event_msg token_count` 的 `info` | **Claude 按 `message.id` 去重**（本机实测一个会话 847 条 usage 记录只对应 415 条消息，不去重翻一倍）；**Codex 取最后一条 `total_token_usage`**（累计量，求和会翻倍）；Claude 无会话总量，`total_tokens` = input + cache_read + cache_creation + output，明写在代码里 |
| 轮次 | Claude：`stop_reason=end_turn` 的**去重后**消息数 / 非 sidechain 的 user 文本消息数；Codex：`turn_context` 条数 / `message role=user` 条数 | 子代理转写现在是自己的会话（§20.5），它自己的轮次正常计 |
| 模型 | Claude `message.model`（`<synthetic>` 不算模型，跳过）；Codex 每个 `turn_context.model`，token 用 `last_token_usage` 逐轮归到当时的模型 | 会话内可多个 |
| 改动量 | Claude `Edit`/`MultiEdit`（old 行计删除、new 行计新增，与 diff 的 `-`/`+` 同一口径）、`Write`/`NotebookEdit`（content 行数计新增）；Codex `apply_patch` 正文的 `+`/`-` 行，按 `*** Update/Add/Delete File` 分文件 | Codex 的补丁写在 exec 脚本的字符串字面量里，换行是转义的 `\n`，解析器先还原再数 |
| 活跃时长 | 相邻记录时间戳的间隔，**间隔 ≤ 10 分钟才计入** | 空闲不计；`session_stats.duration_ms` 仍是首末墙钟时间 |
| 成本 | 只有 opencode 记录，取 normalized 文档的 `session.usage.cost` | 其它来源为 NULL——不是 0 |

`usage_source` 封闭集合：`claude_usage` / `codex_token_count` / `opencode_session` / `dsh_message_usage` / `unrecorded`。**没有 token 记录的会话仍然写一行**：轮次、改动量、活跃时长是记录下来的事实，token 列为 NULL 且 `usage_source='unrecorded'`。会话根本没有度量行（转写已不在磁盘）时，API 同样返回 `source:"unrecorded"` 加一串 null。

**API**：会话列表与详情元素新增 `usage` 对象（详情另带 `by_model`）与 `expected_exit_count`；排序新增 `sort=tokens|lines_changed|active`（无度量行的会话排在最后，不当作 0）；`/overview` 与 `/projects/{key}` 新增 `usage:{known_sessions, token_sessions, in_range, total_tokens, input_tokens, output_tokens, cached_input_tokens, lines_added, lines_removed, files_changed, active_ms, cost_sessions, cost, note}` 与 `by_model:[{model, sessions, turns, total_tokens}]`。分子分母是 `known_sessions / in_range`。

## 20.5 Claude Code 子代理转写 = 子会话（口径统一）

**问题**：Claude Code 的 `subagents/agent-<id>.jsonl` 里每条记录都写着**父会话**的 sessionId，旧读法把子代理的工作并进父会话；Codex 的子代理本来就是独立会话。同一个"会话的工具调用数"在两个 harness 下含义不同。

**新口径**：子代理转写是自己的会话，两个 harness 一致。

| 字段 | 取自 |
| --- | --- |
| `source_session_id` | 文件名里的 `<agentId>`（会话 id `claude_code:<agentId>`） |
| `thread_kind` | `subagent` |
| `parent_session_id` | 记录里写的那个 sessionId，加上 `claude_code:` 前缀 |
| `agent_role` / `agent_nickname` | 父会话里那次 `Agent` 调用的 `subagent_type` / `description`。链接关系是 harness 自己写的：该调用的 tool_result 正文里有 `agentId: <id>`。找不到就留 NULL，不猜 |

**已入库历史怎么办**（`eventstore.RelocateEvents`）：那些事件此前记在父会话名下。重读时**先改归属再重放**：按 `locator.raw_ref`（源位置，不含会话 id）找到父会话里属于这个文件的行，把 `session_id` 与 `source_event_id` 改成子会话的。

**这不是改写历史**：`source_event_id` 是「会话 id + 源位置」的散列，即归属本身的一部分；纠正记录归属必然重算它。行的 `id`、`payload_json`、`locator_json`、`occurred_at`、`ingested_at` 一个字节都不动。改完之后重放同一个文件，id 已经对上，一条也不会重复插入（`session_hierarchy_test.go` 用「重读后事件总数不变 + 无重复 `(session_id, source_event_id)`」断言这一点）。

父子两边的 `event_pairs` / `session_commands` / `session_files` / `tool_call_stats` / `friction_records` 是派生的，随即整体重算；`sidechain:true` 与 `agent_id` 仍留在事件 payload 里。父会话的 `subagent_count` 因此就等于子会话数，与 `?parent=` 返回的行数一致。

## 20.6 harness 注入块不算用户轮

`<environment_context>`、`<user_instructions>`、`<recommended_plugins>`、`<turn_aborted>`、`# AGENTS.md instructions` 这些以 user 角色写进转写、但没有任何人打过的块，不进转写、也不计用户轮。`<turn_aborted>` 与 `event_msg turn_aborted` 说的是同一件事，用户中断以后者为准，不重复计数。

**不静默**：每一类被排除的块按前缀计数，daemon 在每轮 pass 结束时打一行日志（`native history: harness-injected blocks left out of the transcript <tag>=<n> …`）。

## 20.7 投影版本 `session_stats.projection_version`

`projected_at` 只回答"投影跑过没有"。`ProjectionVersion` 回答"跑的是哪一版规则"。工具名匹配改成大小写不敏感（opencode / dsh 写 `bash`/`read`/`edit`/`write`，Claude 写 `Bash`/`Read`/…；**存下来的工具名保留来源原样，只有匹配不区分大小写**）之后，已入库会话靠这个版本号触发重算。

## 20.8 摩擦聚合改为一次装载

摩擦页对同一批记录问八个问题（总数、两个分布、hint 分布、复现数、分组列表、分组数、生命周期）。原来每个问题跑一遍同一个 CTE（friction_records ⋈ sessions ⋈ events，再 union 一次 asset_violation），一次请求要跑七遍。记录集只有几千条，且聚合读的列都很小，所以**只装载一次（不带有界 payload），全部聚合在 Go 里算**。明细视图的记录列表仍然直接读 SQL——只有它需要 payload。

`by_hint_kind` 因此能给出正确的 `session_count`：同一个会话命中同一类的三个签名，是一个会话，不是三个。

## 20.9 迁移运行器的表重建通道

文件首行是 `-- flatline:no-transaction` 的迁移在事务外执行，期间 `PRAGMA foreign_keys=OFF`，执行完 `PRAGMA foreign_key_check` 必须为空才记账。理由：SQLite 不允许在事务内关外键，而本 daemon 常开外键，于是「建新表 / INSERT SELECT / DROP 旧表 / RENAME」会把 DROP 级联到所有子表。以后需要重建表用这个通道，不再动 `writable_schema`。

## 20.10 `display_title` 不带装饰符号（修订 §13.7）

§13.7 的 `synthesized` 形如 `<role> · <nickname> ← <父会话展示名>`。箭头是装饰符号，不该出现在名字里。修订为：

| 字段 | 内容 |
| --- | --- |
| `display_title` | `synthesized` 时只放 `<role> · <nickname>`（没有 nickname 就只有 role）。其余三种 `title_source` 不变 |
| `parent_title` | 新增，父会话的展示名（`title` → `task_text`，截 60 字），任何会话只要记了父会话就有；没有则 `null` |
| `title_source` | 不变（`ai` / `task` / `synthesized` / `none`） |

名字就是名字；父会话叫什么是另一个字段，前端自己决定怎么并排显示。

## 20.11 每个阶段只发布自己的进度块

`ingest/status.import` 里 `pairing` 只在 `phase=="pairing"` 时存在，`reparse` 只在 `phase=="reparse"` 时存在。进入 reparse 时清掉 `pairing`，reparse 结束交回 pairing 时清掉 `reparse`。读的人不必判断两个进度对象哪个是活的。

## 20.12 摩擦 summary 的 `by_harness`

`/friction.summary` 新增 `by_harness: [{key, count, session_count}]`，口径与 `by_category` / `by_tool` 一致：**当前筛选之下**的计数。前端的 harness 选择器因此显示"选它会得到什么"，而不是退回全库口径。

## 20.13 迁移是 append-only

一个迁移一旦被任何数据库记账，就不能再改：改过的文件不会重跑，改动只到达之后新建的库，更早的库留在代码不再期待的 schema 上——daemon 于是在一列"对一半安装存在、对另一半不存在"的字段上启动失败。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `migrations/lock_test.go` | 把每个已发布迁移文件的 sha256 写死 | 改动即红；修复方式永远是加新文件，不是改旧文件 |
| 迁移作者 | 需要补列时新建一个迁移 | 011 之后补的 `cost` / `projection_version` / 索引改名，全部移进 `013_projection_version.sql` |
| 迁移运行器 | 首行 `-- flatline:tolerate-existing` 的迁移逐条执行，只跳过 `duplicate column name` | 用中间版 011 建过的库照样能升到 013，不报错 |

`storage.SchemaVersion = 13`。

---

# 第六阶段方向 · 总览 = 本周摘要（发现线 v2，草案）

> 依据 `docs/qa/dogfood-2026-08-22.md` 第三批价值挖掘。原则：每个数字都有分子分母与口径；只陈述对齐，不写因果；没有总分。

## 22. 总览页重组为"这段时间发生了什么"

| 区块 | 回答的问题 | 数据来源（已有或 A4 在做） | 对比基线 |
| --- | --- | --- | --- |
| 本期 vs 上期 | 会话 / 已记录时长 / 工具调用 / token / 改动行 / 摩擦，各给 Δ 与方向 | `session_stats`、`session_usage`（A4） | 同长度的上一区间 |
| 并行度 | 本期最多同时进行 N 个主会话、发生在何时 | `sessions.started_at/ended_at` 区间重叠 | 上期峰值 |
| 摩擦生命周期 | 新出现 / 仍在发生 / 已消失（已做） | `friction_lifecycle` | — |
| 环境健康 | 缺失命令清单（`command_not_found` 去重）、失败率最高的命令（已记录结果 ≥ 30） | `session_commands` + 退出码语义表（A4） | 上期 |
| 项目动态 | 每个活跃项目：会话/时长/摩擦/热点文件/模型；点击进项目页 | `/projects` | 上期 |
| 子代理使用 | 派出子代理的会话数、平均每会话子代理数、角色分布、子代理摩擦占比 | `sessions.parent_session_id/agent_role` | 上期 |
| 反复读取 | 同一会话内同一文件读 ≥3 次的会话数与次数（"上下文重读"） | `session_files(action='read')` | 上期 |

交互：时间范围选择器决定"本期"；每个区块右上角一个"上期"小字对比；所有数字可点进对应筛选。

## 23. 会话详情补充（管理线）

- 头部增加 usage 条：token（输入/缓存/输出）、轮次、改动行（+/−）、活跃时长 / 总时长；缺失显示"未记录"。
- "命令与文件"里文件表加"重复读取 N 次"列；命令表加"程序家族"筛选（python*/node*）。
- 子会话列表显示每个子会话的 role · nickname、时长、工具调用、摩擦。

## 24. 不做

- 会话"质量分"、项目"健康分"、模型排行榜；
- 任何需要模型推理的摘要。

## 20.14 三条解析口径的更正（修订 §14）

1. **Claude token 去重取最后一条，不是第一条。** §14 说"同一 `message.id` 的多条记录 usage 重复，必须按 message.id 去重"——重复是对的，但前几条是**占位值**：thinking 块、tool_use 块各写一条，只有带 `stop_reason` 的最后一条是这条消息的真实用量。实测 `agent-a33e43580b1766c43.jsonl`：151 个 message.id 里 111 个的 `output_tokens` 在多条记录间增长（`[5, 5, 314]` 这种），按第一条求和 = 14,655，按最后一条 = 161,497。规则改为 **按 message.id 保留最后一条记录**（等价于取 `max(output_tokens)`）。`input_tokens` / cache 字段在各条里相同，取哪条都一样。

2. **空的 tool_result 也是一条结果。** 解析器原先对"输出为空且没有 `is_error`/`exit_code`"的 tool_result 直接跳过，于是 `tool_result_count` 比原文少一条，配对也缺一条。调用回来了就是事实，照常记录（本机重读补回 743 条）。

3. **Claude 子会话的身份读 `agent-<id>.meta.json`。** 转写旁边这个文件是 harness 自己的记录：`agentType` → `agent_role`，`description` → `agent_nickname` 与会话 `title`，`toolUseId` 指向父会话里那次 Agent 调用，`spawnDepth` 是层级深度。它比从父转写反查稳，也让子会话的名字不再是 `<fork-boilerplate>…` 这类注入正文。文件缺失时退回 §20.5 的父会话反查。

同时：`sessions.title` / `task_text` 的写入规则改为"新的一次解析（非空时）覆盖旧值"——两者都是从原文读出来的展示事实，不是用户记录，更新的解析器读得更准。

**审计脚本的两处口径**（`scripts/audit_accuracy.py`，已同步）：Claude 子代理文件里每条记录都是 `isSidechain:true`，按主文件的规则跳过就什么都不剩；原文完全没有 usage 记录的会话，"求和为 0" 不等于"用了 0 个 token"，这类会话 API 返回 `usage_source="unrecorded"` 与一串 null，脚本报的 `0` 是错的。

## 20.15 本轮未做的两项

1. **资产路径引用仍按 basename 匹配。** `assetIndex.invocationsInText` 把只出现一次的 basename 当作对该资产的引用，于是一个项目写 `/tmp/…/taskboard/__init__.py` 会被记成另一个项目 `…/hookify/hooks/__init__.py` 的 `asset_invoked loaded`（b3.db 实测 61 条，另有 4 条误配到 `security-guidance/hooks/llm`）。规则应改为"路径引用必须与资产 `source_path` 全路径相等，或至少两段路径后缀相等"。**没有在本轮改**：收紧规则会改变一次解析产出的 `asset_invoked` 事件集合，而旧规则已经写下的事件是 append-only 的，直接改会让旧的错误事件与新的正确事件并存。它需要先有一个"按当前解析结果作废旧资产证据"的 supersede 通道（可复用 §20.5 的归属更正机制），再和规则一起落。

2. **`session_commands` 仍只记每个 tool_call 的第一条命令。** Codex 的 `const cmds=[[label,cmd,cwd],…]` 数组形态只记第一条；唯一键改为 `(session_id, event_id, ordinal)` 才能记全。**没有在本轮改**：本机 173,337 次 tool_call 里带这种数组的有 713 次，其中真正含多条的只有 1 次（0.0006%），而改动需要重建 `session_commands` 表。留待需要时按 §20.13 的 `-- flatline:no-transaction` 通道做。

---

# 25. A6 实施补充（2026-08-23）— 本周摘要、准确性收尾、多源配置

> 本节是契约的一部分：B4 按此绑定字段。凡与 §14 / §22–§24 冲突的，以本节为准。
> 迁移 014–019，`storage.SchemaVersion = 19`，`history.ParserVersion = "parser/5"`，
> `eventstore.ProjectionVersion = "projection/4"`，`friction.ClassifierVersion` 不变。

## 25.1 总览与项目页的"这段时间"：`current` / `previous` / `delta`

**要回答的问题**：这段时间发生了什么，和上一段一样长的时间比，变了多少？

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| 调用方 | `GET /api/v1/overview?from&to`（或 `/projects/{key}?from&to`） | 响应里多一个 `current`：本区间的 KPI 摘要 |
| 调用方 | 再加 `compare=1` | 多出 `previous`（**同结构**的上一区间摘要）与 `delta`（逐 KPI 的差值与方向） |
| 服务端 | 上一区间 = 同样长度、紧邻本区间之前，上界比本区间下界早 1 毫秒 | 一个会话不可能同时落进两个区间 |
| 服务端 | 本区间没有下界（`from=all`）时 | `previous` 与 `delta` 都是 `null`——没有长度就没有"上一段" |

**什么保证它**：`internal/api/period_test.go` 的 `TestOverviewCompareReportsThePreviousWindowAndItsDelta`
把 `previous.range` 拿回来再直接查一次，断言 `previous.sessions/tool_calls/friction`
与直接查询逐项相等（§16 一致性门禁的扩展）。

**口径**：`current` 与顶层计数是同一个窗口、同一条规则（默认只统计 `thread_kind='main'`
且非空会话），所以 `current.sessions == sessions.in_range`（总览）
/ `== sessions.main_non_empty`（项目页，该字段本轮新增）。
项目页新增顶层 `scope` 对象说明这一点。

**`current` / `previous` 的真实形状**（**扁平对象**，不是顶层那种嵌套结构——
顶层的 `sessions` 是 `{in_range,total,by_harness,…}`，这里的 `sessions` 就是一个整数）：

```jsonc
{
  "range": {"from": "...", "to": "..."},
  "sessions": 114, "projects": 12,
  "events": 0, "messages": 0, "tool_calls": 0, "friction": 0, "sessions_with_friction": 0,
  "duration_ms": 0, "duration_known_sessions": 0,
  "usage_known_sessions": 0, "token_sessions": 0,
  "total_tokens": 0|null, "output_tokens": 0|null,
  "lines_added": 0|null, "lines_removed": 0|null, "active_ms": 0|null,
  "parallelism": {...}, "subagents": {...}, "reread": {...}, "environment": {...}
}
```

**`delta` 的形状**：`{"<kpi>": {"value": <本期 − 上期>, "direction": "up"|"down"|"flat"} | null}`。
键：`sessions`、`projects`、`events`、`messages`、`tool_calls`、`friction`、
`sessions_with_friction`、`duration_ms`、`total_tokens`、`output_tokens`、`lines_added`、
`lines_removed`、`active_ms`、`parallel_peak`、`sessions_with_subagents`、
`subagent_sessions`、`reread_sessions`、`reread_reads`、`missing_commands`、`failing_programs`。

**缺失 ≠ 零**：任一侧没有度量（`token_sessions = 0` 或 `usage_known_sessions = 0`）时，
对应 KPI 在摘要里是 `null`，`delta` 里也是 `null`。没人量过的一段时间不是"用了 0 个 token"。

**容易误读的地方**：*"`previous` 是上周"*——不是。它是**与本区间等长**的紧邻前一段：
`from=30d` 时它是前 30 天，`from`/`to` 给的是 3 天时它就是前 3 天。

## 25.2 四个新区块（`current`/`previous` 内各有一份，同时平铺在顶层）

顶层的 `parallelism` / `environment` / `subagents` / `reread` 就是 `current` 里的同名字段，
平铺是为了 B4 不必先进 `current`。

### 并行度 `parallelism`

```
{peak, peak_at, sessions_considered, unbounded_sessions, note}
```

- **规则一句话**：把每个会话的 `started_at`/`ended_at` 当成一段区间，起点 +1、终点 −1 扫一遍，
  最大的同时开启数就是 `peak`，第一次达到它的时刻是 `peak_at`。
- **分母**：`sessions_considered` = 同时记录了起止时间的会话数；只记了开始的会话进
  `unbounded_sessions`，**不进分母**（未记录 ≠ 同时在跑）。
- 终点与起点同一时刻时先关后开：只是首尾相接的两个会话不算重叠。

### 环境健康 `environment`

```
{missing_commands:[{command, sessions, count, last_at}],
 failing_programs:[{program, calls, known_outcomes, failures, rate}],
 min_known_outcomes: 30, note}
```

- `missing_commands` 来自 `friction_records.category = 'command_not_found'` 的**签名样例行**，
  命令名从行内抽取（`bash: line #: ruff: command not found` → `ruff`；
  `zsh: command not found: pnpm` → `pnpm`；`ls exit 127` → `ls`）。抽不出来的行归
  `__unrecorded__`，不丢弃。按会话数降序，最多 10 条。
  **注意**：样例行已经过签名归一化（小写、数字串变 `#`），所以命令名也是小写、
  `sqlite3` 会显示成 `sqlite#`。
- `failing_programs` 的**分母是 `known_outcomes`**（源头真的记了结果的调用数），不是 `calls`；
  `expected_exit`（`rg` 退出 1 表示没匹配到）算已记录结果、**不算失败**。
  只列 `known_outcomes >= 30` 的程序——两次运行不构成失败率。按 `rate` 降序，最多 10 条。

### 子代理 `subagents`

```
{sessions_with_subagents, subagent_sessions, avg_per_session,
 by_role:[{key,count}], friction_share:{numerator,denominator,rate}, note}
```

- 只统计 `thread_kind='subagent'` 的会话；`sessions_with_subagents` 是它们的
  `parent_session_id` 去重数，`avg_per_session = subagent_sessions / sessions_with_subagents`
  （分母为 0 时是 `null`）。
- `by_role` 只统计子代理自己的 `agent_role`：主会话本来就没有角色，不进这个分布
  （修掉 dogfood 第一轮"角色未记录 123"那条）。
- `friction_share` 给出分子分母：子代理会话的摩擦 / 本区间全部会话的摩擦。
- **这个区块用的是不带主会话过滤的同一个窗口**——主会话过滤要排掉的正是子代理。

### 重复读取 `reread`

```
{sessions, reads, threshold: 3, top_files:[{path, sessions, reads}] ×8, note}
```

同一会话内同一 path 被 `action='read'` 记录 ≥3 次即计入；`reads` 是这些 (会话, 路径) 组的
读取次数之和（不是超出 3 次的部分）。`top_files` 是同一个集合按路径分组、按 `reads` 降序的
前 8 条——总数和清单读的是同一个子查询，两者不可能对不上。

## 25.3 `/api/v1/stats` 带上度量

数据页读 `/stats`，总览读 `/overview`。`/stats` 原来不返回任何 token 字段，于是数据页写
"Token 未记录"而总览同时印着 25.9B——同一个系统两个说法。`/stats` 现在返回与
`/overview` 同结构的 `usage`（含 `definition`）与 `by_model`，口径就是 §25.4 那一条，
范围是全库（没有时间窗）。

## 25.4 时间线分页

`GET /api/v1/timeline?offset=&limit=`（默认 `limit=1000`）新增：

```
"pagination": {offset, limit, total, has_more}
```

排序改为 `occurred_at DESC, row_id DESC, kind`（三张表各自发号，所以 `kind` 是最后的
决胜键）。`clusters` 与 `timeline` 结构不变。

**为什么改**：页面原来靠"加大 limit"看更早的条目，等于把已经拿到的全部重拉一遍。

## 25.5 token 口径统一（修订 §14）

**要回答的问题**：一个 token 总数到底数了什么？

原文核实（本机全量扫描）：

| harness | `input_tokens` 含不含缓存 | 自己的 total | reasoning |
| --- | --- | --- | --- |
| Codex `token_count.info.*_token_usage` | **含**（171,680 个 usage 块里 `cached ≤ input` 恒成立） | `total == input + output` 逐条成立（1,593 个分量全 0 的块除外） | `reasoning_output_tokens` 含在 `output_tokens` 内 |
| Claude Code `message.usage` | **不含**（`cache_read_input_tokens`、`cache_creation_input_tokens` 各自独立；实例 input 2 / cache_read 26,249 / cache_creation 13,595） | 不写会话总量 | `output_tokens_details.thinking_tokens` 含在 `output_tokens` 内 |
| opencode `session.tokens_*` | 五列互不重叠（A5 字段矩阵） | 不写 | `tokens_reasoning` 含在 `tokens_output` 内 |
| dsh `assistant/message.data.usage` | `inputTokens` 与 `cacheReadTokens` 分开（A5 字段矩阵） | 不写 | 不记录 |

**统一存储口径**（`eventstore.TokenTotalRule`）：

- `input_tokens` 一律存**未命中缓存的输入**。Codex 因此存 `input − cached − cache_write`。
- `total_tokens` **一律由分量重算**：`input + cached_input + cache_write + output`，
  不采信任何 harness 自己的 total。
- `reasoning_tokens` 是 output 的一部分，**只报告、不重复加**。
- 聚合端点新增 `usage.definition`，就是上面这一句，供前端 hover 展示。

**什么保证它**：`internal/api/consistency_test.go` 的
`TestTokenTotalIsAlwaysTheSumOfItsComponents`——任一会话 `total == 各分量之和`，
任一聚合 `total == Σ 会话 total`。

**为什么必须改**：混合历史下旧口径不自洽——本机 `from=all` 读到
total 34.71G，而 input 19.28G + cached 33.84G + output 0.10G = 53.2G。
Codex 的 total 把缓存算在 input 里，Claude 的 input 不含缓存，两边直接相加就是重复计数。

**这不是"改了历史"**：`session_usage` 是派生投影，`parser_version` 变化触发全量重算。

## 25.6 会话跨度只增不减（§16 新增一条不变量）

**要回答的问题**：一个会话的"活跃时长"怎么会比它的"总时长"还长？

因为 `sessions.ended_at` 原来用 `COALESCE` 写：**第一次**读到的结束时间会被永久保留，
而 `session_usage.active_ms` 每次重读都按当时的全文重算。一个还在被写入的转写于是出现
"3 分 40 秒的会话里有 50 分钟活跃时间"——本机 15 个会话如此。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `IngestSession` | `started_at` 取**最早**的读法，`ended_at` 取**最晚**的读法（按 `julianday` 比较，不比字符串） | 会话跨度只增不减；身份类字段（harness/model/cwd/title）仍保留首次读到的值 |
| 迁移 019 | 用事件里已有的 `session_stats.first_event_at` / `last_event_at` 把旧规则写下的行补齐，并重算这些行的 `duration_ms` | 不读任何源文件；对"最后一条记录不是事件"的来源（Codex 的 `token_count`）只能补到最后一条事件为止 |
| `RollUpSessionUsage` | 同一份转写（按文件名认身份）只汇总一次 | 软链进两个目录的子代理转写不再翻倍 |
| `ParserVersion` 升到 `parser/5` | 全量重读一次 | 每个会话的跨度与度量按当前规则从原文重新读出，019 补不到的那点尾巴也补齐 |
| §16 不变量测试 | 所有同时有 `duration_ms` 与 `active_ms` 的会话满足 `active_ms <= duration_ms` | 违反即红 |

**为什么这条一定成立**：`active_ms` 是相邻记录间隔（≤10 分钟的那些）之和，
`duration_ms` 是首末之差；若干相邻间隔之和不可能大于首末之差——**前提是两者读的是同一份文本，
而且那份文本只被数了一次**。

**第二个来源：同一份转写被数了两次。** Claude Code 会把一个子代理的转写
**软链**进第二个父会话的目录，于是磁盘上同一个文件有两条路径，
`native_file_usage` 就有两行，汇总时全部翻倍——本机一个子代理因此报了 2×20.3M token、
49 分钟会话里 98 分钟活跃时间。规则：**一份转写按文件名认身份，不按路径**
（Claude 的转写以会话或 agent id 命名，Codex 的 rollout 以自己的 uuid 命名，
opencode 的伪路径以行 id 结尾——文件名就是身份，目录不是），
汇总时同名只取一份。`ParserVersion` 升到 `parser/5` 让已入库的行重算一次。

**容易误读的地方**：*"这样会话时长就会一直变"*——只在转写本身还在增长时变，
而且只朝一个方向。文件写完之后再读多少次都是同一个跨度。

**`in_progress` 也做了**：coordinator 给的是二选一，但两件事回答的是不同的问题。
修根因让数字自洽；`in_progress` 让页面知道这个数字还会变。
会话列表与详情元素新增 `in_progress`：该会话最新一份转写的**指纹** mtime 距现在 < 10 分钟
（与活跃时长的空闲阈值同一个 10 分钟）。前端据此显示"进行中，数字会变"。

**指纹是上一次读到时记下的 mtime，不是此刻 stat 的结果。** 常态下 daemon 每 5 分钟扫一轮，
变化过的文件会被重读并刷新指纹，所以一个真在写的会话会被标出来；但 daemon 正在做一次
长时间重读（本机实测 1165 个文件约 7 分钟）时，refresh 还没轮到，指纹会旧到超出 10 分钟，
`in_progress` 就变回 false。这是"上次看到时"的口径，不是实时的。

**容易误读的地方**：*"`in_progress` 是 harness 告诉我们的"*——不是。
它是"这个文件刚被写过"的读法，没有任何 harness 记录"会话还开着"这件事。

## 25.7 资产路径引用收紧 + 证据作废通道（落 §20.15 第 1 项）

**规则变化**：`assetIndex` 的路径引用从"basename 唯一即算"改为
**全路径相等、项目内相对路径相等，或最后两段路径后缀相等且该后缀在资产索引里唯一**。

**为什么需要一条新通道**：canonical 事件 append-only。旧规则写下的 `asset_invoked`
不能删也不能改 payload，但它又不能和新规则写下的并存——同一段原文会同时带着两种读法。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| 重读（`ReparseStaleTranscripts`） | 记下这一轮每个会话解析出的 `asset_invoked` 的 `source_event_id` | 有了"当前规则会产出什么"的完整集合 |
| 同上，**只对本轮读全了全部转写文件的会话** | 库里有、本轮没产出的事件 → `superseded_at = now` | 行、payload、locator、时间戳一个字节不动；只是标记"后来的读法不再产出它" |
| 同上 | 本轮又产出了、之前被作废的事件 → `superseded_at = NULL` | 通道可逆 |
| 同上 | 某会话某资产**全部** `asset_invoked` 都被作废时，该会话该资产的 participations / opportunities 一并作废；只要还有一条活着就不动 | 派生行跟着证据走 |
| 所有参与 / 机会 / 资产统计 | 默认 `superseded_at IS NULL` | 页面上的数字是当前规则下的数字 |

**为什么要"读全了才作废"**：一个 Claude Code 会话是主转写 + 每个子代理一个文件。
只读了其中一个就作废，会撤掉另一个文件仍然会产出的证据。

**`ReparsableSource` 扩到四个来源**（修订 §20.3）：`history.ReadFile` 现在也能重读
opencode（按 `<db>#<session_id>` 伪路径读回单行）与 dsh（zstd 文件）。原来只有
Claude Code 与 Codex 可重读，于是 opencode 会话的资产证据永远不会被这条通道看到——
dogfood 第六轮记下的那 61+4 条误判正好全在 opencode 会话里。Hermes 只探测、无 reader，
仍然不可重读。

**容易误读的地方**：*"作废 = 删除"*——不是。行还在，下钻仍能回到原文位置；
把 `superseded_at` 清空就完全恢复到旧读法。

**迁移**：014 给 `events`、`participations`、`opportunities` 各加一列 `superseded_at`。

## 25.8 `session_commands.ordinal`（落 §20.15 第 2 项）

唯一键 `(session_id, event_id)` → `(session_id, event_id, ordinal)`，迁移 015 走
§20.9 的 `-- flatline:no-transaction` 通道重建表。Codex 的
`const cmds = [[label, cmd, cwd], …]` 数组现在每条都记一行。

**退出码归谁**：一次工具调用只报告一个状态，shell 报告的是**最后一条语句**的状态，
所以它记在 ordinal 最大的那一行；同一次调用里更早的命令 `exit_code`/`is_error` 为
NULL（未记录），**不是成功**。`session_stats.command_count` 是本表的 COUNT，随投影版本
`projection/4` 重算。

## 25.9 `project_key` / `worktree`

**要回答的问题**：`…/.claude/worktrees/agent-xxxx` 这种目录是一个项目吗？

不是。它是 harness 为一次任务开的临时 checkout。迁移 018 给 `sessions` 加两列并回填：

- `project_key`：cwd 里含 `/.claude/worktrees/<name>` 时取其前缀（仓库根），否则就是 cwd；
  源头没记 cwd 时为 NULL（API 仍报 `__unrecorded__`）。
- `worktree`：被折叠掉的那个名字，否则 NULL。

`/projects`、`/projects/{key}`、`/overview`、`/sessions`、`/sessions/facets`、`/search`、
`/friction` 的项目维度**统一改读 `s.project_key` 列**。会话列表/详情元素新增 `worktree`；
项目页头部新增 `worktree_sessions`（该项目有多少会话跑在 worktree 里）与 `worktrees`（几个）。

**只折叠这一种形态**。`qwen-sm120-runtime-wt-deps`、`qsr-w-b1` 这类靠命名猜的 git worktree
**不动**——猜命名约定会把两个真项目并成一个。

## 25.10 `data_version` 持久化与 ETag 启动戳

**症状**：daemon 重启后计数器从 1 重新开始，浏览器里上一进程缓存的 `/overview` 同号被复用，
总览显示 903 会话而侧栏显示 1164——同一页面两个数。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| 迁移 017 | 建 `meta(key, value, updated_at)` | 有地方写下计数器 |
| daemon 启动 | `LoadDataVersion` 从 `meta.data_version` 续上 | 新进程不会发出旧进程用过的版本号 |
| 每次 refresh 结束 / 用户标注写入 | 先写盘再发布（`BumpDataVersion`） | 崩溃只会跳号，不会重号 |
| 每个 ETag | `"v<version>-<boot>-<hash>"`，`boot` 是本进程独有的启动戳 | 跨进程绝不相等 |
| 用户标注写入 | 走 daemon 的同一个计数器（`api.VersionBumper`） | 一个号覆盖两种写入 |

**什么保证它**：`internal/api/period_test.go` 的 `TestETagCannotRepeatAcrossProcesses`
（两个 Server = 两个进程，ETag 必须不同；同一进程内自己的 tag 仍然 304）与
`internal/eventstore/supersede_test.go` 的 `TestDataVersionSurvivesAReopen`。

**代价**：重启会让浏览器缓存整体失效一次，即使数据没变。这是有意的。

## 25.11 多源配置 `sources`（§17 第一步）

**要回答的问题**：这个会话是从哪台机器的哪个目录读来的？

迁移 016 建 `sources(id, kind, root, label, machine_label, enabled, created_at)`，
`sessions` 加 `source_id`。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| daemon 启动 | 把这次运行要读的每个根登记一行（已存在的行**不覆盖 label**） | 用户能在数据页看到并命名它们 |
| daemon 每轮 refresh 之前 | 从注册表算出这一轮实际要读的根（`ConfiguredHistory`） | `enabled=0` 的根不读；用户新增的根这一轮开始读 |
| daemon 每轮 refresh 之后 | `AttachSessionSources`：按转写文件路径把会话挂到最长匹配的根上 | 会话能说出自己来自哪个根 |
| 用户 `PUT /api/v1/sources` | body `{id, label?, machine_label?, enabled?}`，**只改这三项** | 名字与开关变了；`root` 出现在 body 里直接 400 |
| 用户 `POST /api/v1/sources` | body `{kind, root, label?}`；`kind` 必须是已注册来源，`root` 必须是可读的绝对路径 | 行建好，**下一轮 refresh 才真正去读** |
| `GET /api/v1/sources` | 列出每个根与它已存的会话数、最后一次会话时间 | 数据页表格 |
| `GET /api/v1/ingest/health.sources` | 与注册表按 `(kind, root)` 合并，行上多出 `source_id`/`label`/`machine_label`/`enabled`/`configured` | 探测结果与配置在同一张表里 |

会话列表/详情元素新增 `source_label`、`machine_label`（未挂到任何根时为 `null`——
是"未记录"，不是"本机"）。

**只读**：注册表只说明"去哪里读"，没有任何代码路径会写入源目录。

**容易误读的地方**：*"POST 之后数据就进来了"*——没有。POST 只记录根，
真正读取发生在下一轮 refresh。*"关掉一个源会删掉它的会话"*——不会，
只是不再读那个目录，已经读进来的记录原样留着。

## 25.12 摩擦提示新增 `user_interrupt`

`friction.HintKinds` 增加 `user_interrupt`，规则按 `^user_interrupt\|` 匹配（category
是分类器已经定下的、最可靠的键，所以排在所有读样例行的规则之前），机制文案
"用户主动中断了这一轮，harness 把中断本身记了下来。"
本机第一大签名（403 条 / 56 会话）因此不再显示"字典未覆盖"。

## 25.13 已核对的响应形状（B4 反馈）

- `previous` 是**扁平对象**，`previous.sessions` 是整数（不是 `previous.sessions.in_range`）。
- `delta` 每项是 `{value, direction}`，缺失时整项为 `null`。
- `parallelism.peak_at` 是 RFC3339 字符串或 `null`。
- `subagents.friction_share` 是 `{numerator, denominator, rate}`，`rate` 在分母为 0 时 `null`。
- `reread` 是 `{sessions, reads, threshold, top_files, note}`。
- `/timeline` 的总数在 `pagination.total`，不在顶层。
- `current`/`previous` 都带 `projects`（本区间的项目数），`delta.projects` 随之存在。
- 会话列表/详情元素新增 `in_progress`（布尔）、`worktree`、`source_label`、`machine_label`。

## 25.14 本轮未做

1. **只由任务文本匹配产生、没有对应事件的 opportunity 不在作废通道内。**
   `nativeOpportunityAssetIDs` 会把 `task_text` 里的路径引用也算成机会，而这条路径不写事件，
   所以 25.5 的通道看不到它。新规则下这些机会不会再被创建，但旧行仍在。
   需要一条按会话重算 opportunity 集合的通道，留待后续。
2. **`sources` 的 `enabled=0` 只影响文件型来源与 opencode/hermes 的单一根。**
   同一 kind 配了多个根时，`history.SourceStatuses()` 仍按 kind 报告最后一轮探测结果，
   多根的探测状态没有逐根拆开。
3. **opencode 的五个 token 列互不重叠是按 A5 字段矩阵采信的**，本机没有能独立验证这一点的原文。
4. **`in_progress` 只是"上次读到时文件刚被写过"**（见 §25.6）：没有任何来源记录"会话还开着"；
   用户开着但十分钟没动的会话会显示成不在进行中，daemon 正在长时间重读时也会。
   要做成实时的需要在请求时 stat 一次文件，本轮没做。

---

# 第七阶段 · A7：准确性残留 + 摩擦 → 资产证据桥

> 依据 `docs/qa/dogfood-2026-08-22.md` 第九轮的 "→ A7" 条目与"方向：摩擦 → 资产桥"。
> 原则不变：每条规则一句话可解释，不写因果，缺失 ≠ 零。

## 26. A7 实施补充

### 26.1 程序名解析：shell 语法与内建不是程序

**要回答的问题**：`session_commands.program` 这一列写的是什么？

写的是**这条命令行真正启动的第一个程序**。在此之前它写的是"第一个不是环境赋值、不是 sudo/time/nohup
的词"，于是 `set -euo pipefail` 的程序名是 `set`、续行符 `\` 是程序名、注释行 `#` 是程序名——
本机失败率榜上 `set` 排第 12、`which` 第 19、`\` 第 26，把真正的工具挤到下面。

`friction.Program` 现在按三类跳过：

| 类别 | 规则 | 例子 |
| --- | --- | --- |
| **语句终止**（跳过本语句余下部分，继续读下一条语句） | 只改变 shell 自身状态的内建（`cd`/`pushd`/`popd`/`export`/`set`/`unset`/`alias`/`unalias`/`declare`/`typeset`/`readonly`/`local`/`shift`/`source`/`.`/`trap`/`ulimit`/`umask`）、什么都不做的内建（`true`/`false`/`:`）、判断表达式（`test`/`[`/`[[`）、只探测存在性的内建（`which`/`type`/`hash`）、后面跟词表而不是命令的关键字（`for`/`select`/`case`）、以及注释 `#` | `set -euo pipefail\nmkdir -p /tmp/x` → `mkdir` |
| **词跳过**（跳过这个词，继续读同一条语句） | 运行别的程序的前缀（`sudo`/`time`/`nohup`/`command`/`builtin`/`exec`）、续行符 `\`、语句关键字（`if`/`elif`/`then`/`else`/`fi`/`while`/`until`/`do`/`done`/`esac`/`!`/`{`/`}`）、以及任何以 `-` 开头的词（选项永远不是程序名） | `do sleep 5` → `sleep`；`command -v go` → `go` |
| **软名**（本行没有别的程序时才作数） | `echo` 与 `env`。`env` 是包装器，跳过它继续读同一条语句；`echo` 会打印，它的参数不是程序，跳到下一条语句 | `echo y \| pip install x` → `pip`；`echo "waiting"` → `echo` |

另外两条：`NAME=$(cmd …)` 里真正运行的是 `cmd`（`SNAP=$(ls -d …)` → `ls`，此前是 `-d`）；
`\grep`（转义掉别名的写法）读作 `grep`。

**结果**：命令行只跑了 shell 内建/语法时**没有程序名**（`program` 为 NULL）。这不是"未记录"——
源头把命令行完整记下来了，是这条命令行确实没启动任何程序。`environment.failing_programs`
因此**整个排除 `program` 为空的记录**：对"没有程序"算失败率排不出任何东西。`/tools.programs`
仍然把它作为一个显式桶列出（key 仍是 `__unrecorded__`），因为它是一个真实的调用量。

**什么保证它**：`internal/friction/command_test.go` 的
`TestProgramSkipsShellSyntaxAndBuiltins` / `TestProgramKeepsEchoAndEnvOnlyWhenTheyAreTheWholeLine`；
`ProjectionVersion = "projection/5"` 让所有会话的命令投影重算。

**容易误读的地方**：*"`echo` 从此不会出现在 /tools.programs 里"*——会。
一整行只有 `echo` 时它就是这行运行的程序；变的是"`echo …  && find …`"这种行不再算成 echo。

### 26.2 `which X` 的失败是"命令不存在"的证据

**要回答的问题**：`which ruff` 退出 1 说明了什么？

说明 `ruff` 不在这次会话的 PATH 里。这条记录此前谁也没读：它不产生 `command not found`
文本，分类器把它归成 `nonzero_exit`，`environment.missing_commands` 只读
`category='command_not_found'` 的样例行，看不到它。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `friction.ProbedCommand` | 判断一条命令行是不是"**只有一个语句、且形如 `which X`（恰好一个参数）**" | 是 → 返回 X；否则返回 false |
| `/overview.environment` | 对窗口内退出码非 0（且非预期非零退出）的这类调用，把 X 记进 `missing_commands` 的同一批桶 | 缺失命令表多了一路证据 |
| 名字归一 | X 过一遍签名的 `NormalizeLine`（小写、数字串 → `#`） | `sqlite3` 与样例行来的 `sqlite#` 落进同一个桶，而不是两行 |

**为什么要"只有一个语句、恰好一个参数"**：退出码属于**最后一条语句**（§25.8）。
`which gh && gh auth status` 的退出码是 `gh auth status` 的，拿它说 gh 不存在是错的；
`which nsys ncu` 退出 1 只说明"两个里至少有一个不在"，说不出是哪个。两种情况都不记。

**容易误读的地方**：*"missing_commands 是本机缺失命令的清单"*——不是。
它是**这段时间里被记录下来的"没找到"事实**的清单；一个命令装好之后旧记录仍在窗口里。

### 26.3 `__unrecorded__` → `__unparsed__`（missing_commands 专用）

`missing_commands` 里"行里抽不出命令名"的桶改 key 为 `__unparsed__`，语义是
**"输出里抽不出命令名"**——源头记了这行，是这行读不出名字。它与 `__unrecorded__`（源头没记）
不是一回事，此前共用一个 key 是错的。排序上它**永远排在所有具名命令之后**，不管会话数多少。

前端在过渡期同时识别两个 key。

### 26.4 `health.sources`：一个根一行

**症状**：同一个根出现两行——注册表行（`root=/home/bot/.claude/projects`，
`status="configured"`）与一个没有 root 的探测/存量行（`root=""`，`status="not_scanned"`）。
第一次扫描完成前一直是这样。

新规则：**注册表行就是那一行**，探测结果并进去。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| 注册表（`sources` 表） | 每个已登记的根建一行，`status="configured"` | 行上带用户给的名字与开关 |
| 本轮探测（`history.SourceStatuses()`） | 按 **root 归一后相等**（`EvalSymlinks` + `Clean`）找到对应行，写入 `status`/`sessions`/`last_seen_at`/`detail`/`error` | 探测状态并入注册表行，不新增行 |
| 探测没报 root（或注册行没有 root），且该 kind 只有一行 | 也并入那一行 | 首次扫描前不再出现"无 root 的第二行" |
| 探测报的 root 与已有行都不同 | 单独一行 | 同一 kind 的两个根仍是两行 |
| 某 kind 有存量会话但一行都没有 | 补一行 `status="not_scanned"` | 重启后尚未扫描时不显示成 not_found |

§25.11 的状态表因此补上 `configured` 一档：

| `status` | 含义 |
| --- | --- |
| `ok` | 本轮探测到了这个根，并且里面有会话 |
| `no_sessions` | 探到了根，里面没有会话 |
| `not_found` | 根不存在或读不到 |
| `not_scanned` | 有存量会话，但本进程还没扫过，且这个根没登记在注册表里 |
| `configured` | 根登记在注册表里，本进程还没探测过它（刚 POST 的根，或首次扫描尚未跑完） |

**容易误读的地方**：*"`configured` = 配好了、在读了"*——不是。它只说明"登记了"，
真正读发生在下一轮 refresh 之后，那时它才会变成 `ok` / `no_sessions` / `not_found`。

### 26.5 opportunity 的作废通道（落 §25.14 第 1 项）

§25.7 的证据通道跟着 `asset_invoked` 事件走。但 `nativeOpportunityAssetIDs` 会把
**task_text 里的路径引用**也算成机会，而这条路径不写任何事件——旧路径规则收紧之后，
证据被作废了，这些机会仍然站着。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `ingest.Pipeline.Ingest` | 在 `Report.Opportunities` 里报出本次解析产出的**完整** `(shape_class, asset_id)` 集合 | 有了"当前规则会产出哪些机会"的全集 |
| `ReparseStaleTranscripts`，**只对本轮读全了全部转写的会话** | 库里有、本轮没产出的 `(shape_class, asset_id)` → `superseded_at = now` | 与 asset_invoked 同一条规则、同一个门槛 |
| `RecordSessionShape` 的 `ON CONFLICT` | 再次产出时把 `superseded_at` 清空 | 通道同样可逆 |

键是 `(shape_class, asset_id)` 而不是 `asset_id`：任务形状规则变了以后，
旧形状下的行也该退场，只比对 asset 会把它留下。

**容易误读的地方**：*"作废 = 这个资产没机会了"*——不是。作废的是**这一条按旧规则写下的机会行**；
同一个资产在别的会话里的机会不受影响。

### 26.6 软链转写只读一次

Claude Code 会把一个子代理转写**软链**进第二个父目录，同一份文件于是被走到两次、
读两次、度量两次（§25.6 记的 `active_ms > duration_ms` 第二个根因）。

- `history.jsonlFilesForRoots` 对每个走到的文件做 `filepath.EvalSymlinks`，按**真实路径**去重登记；
  解析不了的路径原样保留。
- `App.PruneLinkedNativeFiles` 在 daemon 启动时删掉"路径本身是软链"的 `native_files` 行。
  这行不是无害的：它让会话看起来多了一份转写，而 §25.7 的证据通道只在
  "本轮读全了这个会话的全部转写"时才动手——多出来的一行会让这个会话**永远不被对账**。

本机重复数 = 1（`agent-a86083b9c621acea7.jsonl` 同时挂在两个父会话目录下）。

### 26.7 摩擦 → 资产：hook 参与的证据桥

**要回答的问题**：hook 这类资产，在会话里留下过什么痕迹？

留下过一种：**hook 拦截记录**。harness 只有在问过 hook、拿到回答之后才会写
"Command blocked by PreToolUse hook: …"。这就是它参与了这次会话的记录。

**规则一句话**：**hook 拦截记录出现在会话中 = 该 hook 参与了该会话。**

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `eventstore.LinkHookFriction`（每次 ingest，按会话重写） | 取该会话中 hint kind 为 `user_hook` 的 friction 记录，从记录文本里抽出 hook 标识 | 候选集 |
| 匹配 | **全路径相等**，或**文件名在 hook 资产里唯一**（`friction.HookReferences` 只认带 hook 后缀名的路径/文件名：`.py .sh .js .mjs .cjs .ts .json .bash .zsh`） | 命中 → 一条 `asset_friction_links(asset_id, friction_id, rule)`（迁移 020）；**匹配不到就不记，不猜** |
| `ingest.Pipeline` | 为每条链接写一条 `participation_signal='observed-use'`、`observation_level='observed-use'` 的参与，locator 指向该 friction 记录的原文位置 | 资产墙上 hook 有了真实的生命体征 |
| 同上 | 把该 hook 加进本会话的 opportunity 资产集合 | 有分母：否则 `vital` 仍然判 `no_opportunity` |

为什么是 `observed-use` 而不是 `invoked`：被记录下来的是 **harness 转述 hook 的回答**，
是从外部观察到的一次使用；hook 自己的执行从来没有被记录过。

**API**：
- 资产列表元素新增 `friction_link_count`（int）。
- 资产详情 `asset.friction_links[]`：`{friction_id, signature, sample_line, session_id, session_title, event_id, occurred_at, rule}`，
  以及同一层的 `friction_link_count`。
- `friction_links` **只在详情端点出现**：列表行里这个字段不存在（"这个响应不回答这个问题"），
  详情里没有链接时是 `[]`（"回答是：没有"）。两者不是一回事。

**容易误读的地方**：*"hook 资产没有 friction_links 就是这个 hook 没生效"*——不是。
绝大多数拦截消息**根本不写 hook 的名字**（只写 hook 自己那句话），匹配不到就不记。
没有链接说明的是"没有可归属的记录"，不是"没有参与"。

### 26.8 `harness_rule` 不连资产，改报规则覆盖缺口

`file has not been read yet` 这类摩擦的机制是 **harness 自己的规则**，不是任何用户资产，
所以它**不产生**任何资产链接。它回答的是另一个问题：用户写的规则里，有没有提到这条机制？

`/friction.summary` 新增 `coverage_gaps[]`，每项 `{signature, sample_line, session_count, mechanism, hint_kind}`，
按会话数降序取前 10。

| 谁做 | 做什么 | 结果 |
| --- | --- | --- |
| `friction.hints` 表 | 给 `harness_rule` 的规则挂上关键词（`file has not been read yet` → `Read before Edit` / `read before you edit` / `read the file first` / `先读后写` / `先读再改`） | 有了可检索的机制关键词 |
| API | 读取所有 `rule` / `agents_md` 资产的**源文件正文**（只读，前 256 KB），大小写不敏感地找这些关键词 | 得到"哪些关键词被至少一个用户规则提到" |
| 判定 | 签名满足：**≥2 个会话**（反复出现）+ 机制是 `harness_rule` + 它的关键词**一个都没被提到** | 记一条 coverage gap |
| 缓存 | 按 rule/agents_md 资产数与最大 `asset_versions.id` 作缓存键 | 每个请求不会重复读 126 个文件 |

**这是事实陈述，不是建议**：它说"这条摩擦反复出现，而你的规则里没有提到这个机制"，
既不说"写一条规则就能解决"，也不说"写了规则就不会再出现"——后者本来也不成立，
这正是两个事实分开报的原因。

**容易误读的地方**：*"coverage_gaps 为空 = 没有反复摩擦"*——不是。
为空可能是"确实有规则提到了它"（本机就是这种情况），也可能是"这类摩擦本来就不到 2 个会话"。
`by_hint_kind` 里的 `harness_rule` 一栏才是这类摩擦的总量。

### 26.9 `/overview.scope.key`

`scope` 是一个对象（`main_sessions_only` / `excludes_empty` / `note`），前端要判断"当前是不是全量"
只能自己拼两个布尔。新增 `key`，取值 `"main_non_empty"`（默认）或 `"all"`（`include=all` 时），
两个布尔原样保留。

### 26.10 版本号

- `history.ParserVersion = "parser/6"`：软链去重与 hook 证据桥都要重读全部转写才生效。
- `eventstore.ProjectionVersion = "projection/5"`：程序名规则变了，命令投影全部重算。
- `storage.SchemaVersion = 20`：迁移 `020_asset_friction_links.sql`。

### 26.11 本轮未做

1. **hook 之外的资产不进证据桥。** 规则只覆盖 `user_hook` 这一个 hint kind；
   skill / rule / agents_md 的参与仍然只来自 `asset_invoked`。
2. **拦截消息不写 hook 名时无从归属。** 本机 49 条 `blocked by PreToolUse hook` 记录**一条都没有**
   写出 hook 的名字或脚本路径（拦截它们的 hook 也不在资产注册表里，是 Codex 侧的配置），
   所以本机实测链接数为 0。规则本身有 fixture 回放测试覆盖。
3. **coverage_gaps 只覆盖 `harness_rule`。** 目前字典里只有一条 `harness_rule` 规则，
   所以最多产生一两条缺口；扩表就会扩覆盖面。
4. **规则正文按全局判定，不分项目。** "有没有规则提到"看的是全部 rule/agents_md 资产，
   不区分摩擦发生在哪个项目——一个项目的规则会替另一个项目"挡下"这条缺口。
5. **`session_commands` 里从 heredoc 正文抽出的假命令没处理。** `cat > x.cu <<'EOF'` 之后的
   代码行仍会被当成语句读，`No` / `Event` / `PY` 这类词还在程序名列里（本机各 ≤42 次）。

---

# 27. 当前 API 契约总表（2026-08-23 收口，以代码为准）

**这一章和 §1–§26 是什么关系。** §1–§26 是各阶段动手前写的设计与动手后写的实施补充，
里面同一个端点被改过多次（`summary=1` 加了又删、`source` 改名 `harness`、token 口径重定义）。
**读契约只读这一章**；§1–§26 保留为历史，用来回答"当初为什么这么定"，不用来回答"现在是什么"。

本表逐个端点核对过 `internal/api/*.go` 与一个真实库（1167 会话 / 406,878 事件 / 931 资产）上的实际响应。
凡是本表与旧章节冲突的，以本表为准。

## 27.0 全局约定

| 约定 | 内容 |
| --- | --- |
| 前缀 | 全部端点在 `/api/v1` 下；`GET /healthz` 是唯一例外（不带前缀，`HEAD` 亦可） |
| 绑定 | 只监听 `127.0.0.1`（ADR-1/ADR-2）；无鉴权、无 CORS、无上传 |
| 编码 | 请求与响应都是 `application/json; charset=utf-8`；写端点用 `DisallowUnknownFields`，多写一个字段就是 400 |
| `limit` | 缺省或 ≤0 时取该端点自己的默认值；上限一律 5000；`/sessions` 系列另有 200 的硬上限 |
| `offset` | 缺省 0，上限 10^9 |
| `data_version` | 读端点大多带这个整数。它随每次会改变数据的导入或写入递增，并持久化在 `meta` 表里，重启不回退 |
| ETag | 走 `s.tagged` 的端点带 `ETag: "v<data_version>-<boot>-<hash>"`；`boot` 是进程启动戳，所以**两个进程不会给出同一个 ETag**，换二进制后浏览器不会命中旧缓存 |
| 响应缓存 | 走 `s.cached` 的端点在 `data_version` 不变时复用上次的响应体；`/ingest/health` 与 `/ingest/refresh` 永不缓存（它们在 `data_version` 不动的时候也会变） |
| "未记录" | 数据源没写的字段一律 `null`，不补 0。JSON 里字段缺席表示"这个响应不回答这个问题"，`[]` 表示"回答了，答案是没有" |
| 时间 | 一律 UTC，RFC3339Nano |
| `from` / `to` | **每个带时间窗口的端点读法一样**（`internal/api/api.go` 的 `rangeBound`），见 §27.10 |
| `*_en` 字段 | 凡是 daemon 自己写的解释性句子（`note` / `definition` / `mechanism` / `category_rule` / `rule` / `coverage_gap_note`），都有一个同名加 `_en` 的兄弟字段，内容是同一句话的英文写法。中文字段一个字没改。**不是事后翻译**：两句话由同一条规则同时产出，见 §27.11 |
| `__unrecorded__` | 分面/分组里"源头没记这一维"的显式键（不是"未知项"的兜底桶） |
| `__unparsed__` | 只用于 `environment.missing_commands`：命令行在，但没有一条规则能从中抽出程序名 |

## 27.1 端点一览

| 方法 | 路径 | 缓存 | 一句话 |
| --- | --- | --- | --- |
| GET/HEAD | `/healthz` | 无 | 进程活着吗、库连得上吗 |
| GET | `/api/v1/overview` | cached | 这段时间发生了什么 |
| GET | `/api/v1/insights` | cached | 代价与机会：中断上下文、零改动高投入、卡死循环、重复读取、覆盖缺口、缺命令（ADR-20，见 §27.13） |
| GET | `/api/v1/signature-watches` | tagged | 签名修复验证列表（读取时评估，ADR-21，见 §27.14） |
| POST | `/api/v1/signature-watches` | 写 | 开始验证一条签名（需显式确认） |
| POST | `/api/v1/signature-watches/cancel` | 写 | 取消验证（保留记录，需显式确认） |
| GET | `/api/v1/sessions` | tagged | 会话列表（筛选/排序/分页） |
| GET | `/api/v1/sessions/facets` | tagged | 当前筛选下每个维度还剩多少 |
| GET | `/api/v1/sessions/export` | 无 | 当前筛选导出 JSON / CSV |
| GET | `/api/v1/sessions/{id}` | tagged | 会话详情（事件/命令/文件/子会话/摩擦） |
| PUT | `/api/v1/sessions/{id}/annotation` | 写 | 置顶 / 备注 / 用户标签 |
| GET | `/api/v1/sessions/{id}/events/{event_id}` | tagged | 单条事件的完整 payload |
| GET | `/api/v1/friction` | cached（仅无参时） | 摩擦总览 / 分组 / 单组详情 |
| GET | `/api/v1/projects` | cached | 项目列表 |
| GET | `/api/v1/projects/{key}` | cached | 项目页 |
| GET | `/api/v1/search` | tagged | 跨资产/会话/项目/程序/摩擦类别的检索 |
| GET | `/api/v1/tools` | cached | 工具与程序的调用与失败 |
| GET | `/api/v1/stats` | cached | 全库计数 + 度量 |
| GET | `/api/v1/stats/time` | cached | 按小时/星期/周的活动 |
| GET | `/api/v1/timeline` | cached | 状态迁移 + 环境变化 + 资产版本，同轴 |
| GET | `/api/v1/notifications` | cached | 由状态迁移投影出的通知 |
| GET | `/api/v1/assets` | cached | 资产列表 / 生命体征墙 |
| GET | `/api/v1/assets/{id}` | tagged | 资产诊断页 |
| GET | `/api/v1/assets/{id}/transitions` \| `/opportunities` \| `/participations` \| `/dispositions` \| `/references` \| `/source` | tagged | 资产的六个下钻 |
| POST | `/api/v1/assets/{id}/dispositions` | 写 | 处置（需显式确认） |
| POST | `/api/v1/assets/{id}/restore` | 写 | 撤销归档（需显式确认） |
| GET | `/api/v1/cleanup` | tagged | 批量清理候选 |
| POST | `/api/v1/cleanup` | 写 | 批量归档（需显式确认） |
| GET | `/api/v1/sources` | tagged | 数据源注册表 |
| PUT / POST | `/api/v1/sources` | 写 | 改名/开关 · 新增一个源根 |
| GET | `/api/v1/ingest/status` | 无 | 导入进度（UI 轮询这个） |
| GET | `/api/v1/ingest/health` | 无 | 数据体检：计数、未记录项、源状态、库大小 |
| POST | `/api/v1/ingest/refresh` | 无 | 请求跑一轮导入 |

`/` 及其余路径交给内嵌 SPA。

## 27.2 会话

### `GET /api/v1/sessions`

| 参数 | 取值 | 说明 |
| --- | --- | --- |
| `project` | 可重复 | 多个值是 OR；`__unrecorded__` 匹配 `project_key IS NULL` |
| `harness` | 可重复 | `claude_code` / `codex` / `opencode` / `dsh` / `hermes` |
| `model` | 可重复 | OR |
| `tag` | 可重复 | OR |
| `from` / `to` | 见 §27.10 | 四种写法：日期、RFC3339、相对（`7d`/`12w`/`6m`）、`all` |
| `thread` | `main`（默认）/ `subagent` / `all` | 默认只列主会话 |
| `empty` | `no`（默认）/ `yes` / `all` | 默认排除空会话 |
| `parent` | 会话 id | 只列这个会话的子会话 |
| `role` / `program` / `file` | 字符串 | 按子代理角色 / 命令程序 / 触碰过的文件筛 |
| `has_friction` / `pinned` | `1` | 开关 |
| `q` | 字符串 | 标题/任务文本检索；加 `deep=1` 连正文一起搜（FTS） |
| `sort` | `recent`（默认）/ 其余见 `sessionSortOrder` | 未知值 → 400 |
| `limit` / `offset` | 整数 | 默认 50，**上限 200** |

响应：`{ sessions: [...], pagination: {offset, limit, total, has_more}, data_version }`

会话行字段（46 个，`internal/api/sessions.go` 的 `sessionResponse`）：

- **身份**：`id`、`source`、`source_session_id`、`title`、`task_text`、`display_title`、`title_source`、`parent_title`
- **时间**：`started_at`、`ended_at`、`duration_ms`、`in_progress`
- **环境**：`harness_version`、`model`、`cwd`、`project_key`、`project_label`、`worktree`、`source_label`、`machine_label`
- **计数**：`event_count`、`transcript_count`、`message_count`、`user_message_count`、`tool_call_count`、`tool_result_count`、`command_count`、`failed_command_count`、`file_count`、`asset_count`
- **摩擦**：`friction_count`、`tool_error_count`、`nonzero_exit_count`、`expected_exit_count`
- **层级**：`thread_kind`（`main` / `subagent`）、`parent_session_id`、`agent_role`、`agent_nickname`、`originator`、`subagent_count`
- **用户标注**：`tags`、`pinned`、`note_preview`
- **检索命中**（只在带 `q` 时出现）：`match_count`、`match_snippet`
- **度量**：`usage`（见 §27.6）
- `is_empty`

### `GET /api/v1/sessions/facets`

参数与 `/sessions` 相同（每个维度的计数都在**排除它自己**的筛选下算，所以选它会得到多少就是显示多少）。

响应：`total`、`projects`、`harnesses`、`models`、`tags`、`programs`、`roles`、`threads`、
`empty {yes,no}`、`friction {with,without}`、`pinned`、`date_histogram`、`data_version`。

### `GET /api/v1/sessions/{id}`

| 参数 | 说明 |
| --- | --- |
| `events` | `page` 时事件走 `limit`/`offset` 分页并压缩 payload；不给就一次返回全部事件 |
| `limit` / `offset` | 事件分页，默认 limit 1000 |

响应：`session`（同上，不含 `usage` 与检索命中）、`events`、`commands` + `commands_total`、
`files` + `files_total`、`children`、`parent`、`friction {count, records, complete, records_truncated}`、`data_version`。

### `PUT /api/v1/sessions/{id}/annotation`

请求 `{ pinned?: bool, note?: string, tags?: string[] }`——三个字段都是指针，**不给就不动**。
`note` 上限 4000 字符（超出 400）；请求体上限 64 KiB。
**只写本地库，绝不碰原始转写文件。**

### `GET /api/v1/sessions/export`

`format=json`（默认）或 `csv`，其余值 400。筛选参数与 `/sessions` 完全相同，但忽略分页，导出到 `sessionExportLimit`。

## 27.3 摩擦

### `GET /api/v1/friction`

一个端点三种形态：

| 形态 | 怎么触发 | 返回什么 |
| --- | --- | --- |
| 总览 + 分组（默认） | 不带 `view` | `summary` + `groups` + `projects` + `pagination` |
| 单组详情 | `view=detail` 或 `detail=1`（**必须同时给 `project` 和 `harness`**，否则 400） | `group` + `summary` + `records` + `pagination` |
| —— | 组不存在 | 404 |

参数：

| 参数 | 取值 |
| --- | --- |
| `harness`（旧名 `source` 仍受理） | `claude_code` / `codex`；其余值 400 |
| `kind` | `tool_error` / `nonzero_exit` / `user_interrupt` / `asset_violation` |
| `category` / `tool` / `signature` | 字符串；`__unrecorded__` 匹配该列为 NULL |
| `project` | 项目 key |
| `q` | 在项目/cwd/harness/会话标题/任务文本/工具名/类别/payload 里做 LIKE |
| `from` / `to` | 见 §27.10，与其它端点同一读法（`from=all` 与相对写法都受理） |
| `group` | `signature`（默认给出生命周期）/ `category` / `tool` / 项目+harness |
| `sort` | `friction`（默认）/ `recent` / `sessions` |
| `window` | 生命周期窗口天数，默认 7 |
| `limit` / `offset` | 分组分页 |

`summary`：`total_events`、`tool_error_count`、`nonzero_exit_count`、`asset_violation_count`、
`user_interrupt_count`、`tool_unrecorded_count`、`session_count`、`project_count`、`recurring_signatures`、
`expected_exit_count`、`by_category`（行内 `rule` / `rule_en`）、`by_tool`、`by_harness`、`by_hint_kind`、
`coverage_gaps`（按（签名 × 项目）列出的缺口，行内 `project_key` / `mechanism` / `mechanism_en`）、`coverage_gap_note`、`coverage_gap_note_en`、`complete`。

分组行：`key`、`group_by`、`label`、`project_key`、`project_label`、`cwd`、`harness`、`category`、`category_rule`、`category_rule_en`、
`tool_name`、`signature`、`sample_line`、`friction_count`、`count`、四个分项计数、`session_count`、`project_count`、
`first_occurred_at`、`last_occurred_at`、`hint`、`status`、`sessions_last_7d`、`count_last_7d`、`days_active`。

`status` 是签名的生命周期，四取一：

| 值 | 判定（按这个顺序取第一个命中，`internal/api/friction_lifecycle.go`） |
| --- | --- |
| `new` | `first_occurred_at >= 窗口起点` |
| `active` | 否则，`count_last_window > 0` |
| `quiet` | 否则，`session_count >= 2`——出现在两个以上会话里，但窗口内安静了 |
| `once` | 否则——只在一个会话里出现过，且窗口内没再发生 |

**容易误读的地方**：*"`once` 是只发生过一次"*——不是。它是**只在一个会话里**发生过；
同一个会话里连着撞十次仍然是 `once`。分辨"反复出现"用的是会话数，不是次数。

**容易误读的地方**：*"`expected_exit_count` 是一类摩擦"*——不是。它是**被排除掉的**那部分：
`rg` 退出 1 表示"没匹配到"，是答案不是失败。默认这些记录不进上面任何一个计数，
这个字段是告诉页面"排除了 N 条"，只有显式 `category=expected_exit` 才把它们列出来。

## 27.4 总览与项目

### `GET /api/v1/overview`

| 参数 | 说明 |
| --- | --- |
| `from` / `to` | 窗口，见 §27.10；缺省下界等于 `from=30d`（同一个窗口，不是近似） |
| `compare` | `1` / `true` 时额外返回 `previous` 与 `delta`（窗口不可比时两者为 `null`） |
| `include` | `all` 时把子代理与空会话也算进来 |
| `window` | 反复摩擦的生命周期窗口天数 |

响应分三层：

1. **顶层平铺**：`range`、`scope`、`sessions`、`projects`、`events`、`messages`、`tool_calls`、`friction`、
   `duration`、`usage`、`by_model`、`assets`、`activity_by_day`、`last_event_at`、`scratch_files`、
   `top_projects`、`top_tags`、`top_programs`、`top_friction_categories`、`top_friction_tools`、
   `hot_files`、`recent_sessions`、`recurring_friction`、`friction_lifecycle`、
   `parallelism`、`environment`、`subagents`、`reread`、`data_version`。
2. **`current`**：同一批数字按"这段时间"重算一份（含四个区块）。
3. **`previous` / `delta`**：只在 `compare=1` 且上一窗口可比时非空。`delta` 的每一项是 `{value, direction}`。

`scope.key` 两取一：`main_non_empty`（默认）或 `all`（`include=all`）。两个布尔 `main_sessions_only` / `excludes_empty` 原样保留。

**跨端点的会话数等式，只在口径对齐时成立**：

```
health.counts.sessions
  == overview(from=all&include=all).sessions.total
  == overview(from=all&include=all).sessions.in_range
  == sessions/facets(thread=all&empty=all&from=all).total
  == sessions(thread=all&empty=all&from=all).pagination.total
  == stats.session_count
  == Σ projects[].sessions
```

`internal/api/consistency_test.go::TestEveryEndpointCountsSessionsTheSameWay` 在 CI 里断言这一组。

**容易误读的地方**：*"默认参数下这几个数就该相等"*——不该。
`/overview`、`/sessions`、`/sessions/facets` 的默认口径都是"主会话且非空"，
`health.counts.sessions` 与 `stats.session_count` 是整张表。
本机默认口径 472、全量 1167，两边都对，只是回答的不是同一个问题。

四个区块：

| 区块 | 关键字段 |
| --- | --- |
| `parallelism` | `peak`、`peak_at`、`sessions_considered`、`unbounded_sessions`、`note` |
| `environment` | `missing_commands`、`failing_programs`、`min_known_outcomes`、`note` |
| `subagents` | `sessions_with_subagents`、`subagent_sessions`、`avg_per_session`、`by_role`、`friction_share`、`note` |
| `reread` | `sessions`、`reads`、`threshold`、`top_files`、`note` |

**容易误读的地方**：*"`missing_commands` 里排最后的 `__unparsed__` 是最少的一类"*——不是。
它是**沉底的**：命令行没写出程序名的记录归到这个桶，排序规则强制它排在所有具名命令之后，与它的计数无关。

### `GET /api/v1/projects` · `GET /api/v1/projects/{key}`

列表行：`key`、`label`、`cwd`、`sessions`、`friction_count`、`first_started_at`、`last_started_at`、`harnesses`、`is_home_dir`。

项目页参数 `compare` / `window` 与 `/overview` 一致，响应结构也一致（`current` / `previous` / `delta` + 四个区块），
另有 `project`（`worktrees`、`worktree_sessions`、`originators`…）、`friction`（含 `lifecycle` 与 `recurring`）、
`roles`、`tags`、`models`、`by_week`、`hot_files`、`outside_project_files`、`top_programs`、`recent_sessions`。

`{key}` 是 URL 编码的绝对路径。

## 27.5 工具、统计、时间线、通知

| 端点 | 参数 | 响应 |
| --- | --- | --- |
| `/tools` | `from` / `to` | `tools[]`（`tool_name`、`harness`、`calls`、`sessions`、`known_outcomes`、`failures`）、`programs[]`（另有 `expected_exits`、`family`）、`range` |
| `/stats` | 无 | `asset_count`、`version_count`、`session_count`、`event_count`、`opportunity_count`、`participation_count`、`state_counts`、`source_counts`、`observation_levels`、`activity_by_day`、`last_event_at`、`usage`、`by_model` |
| `/stats/time` | `tz_offset_minutes` | `hour_weekday`（7×24）、`by_day_of_week`、`by_week`（`week`、`sessions`、`duration_ms`、`tool_calls`、`friction`）、`range`、`scope`、`tz_offset_minutes` |
| `/timeline` | `limit`（默认 1000）、`offset`、`from` / `to` | `timeline[]`（`kind`、`asset_id`、`occurred_at`、`evidence`、`alignment`、`state`）、`clusters[]`、`range`、`pagination`。窗口只切 `timeline[]`，不切 `clusters[]`：一个聚簇是锚点与它前后迁移之间的对齐关系，从中间切断会报出一个比实际观测更窄的对齐 |
| `/notifications` | `limit` | `notifications[]`（`id`、`asset_id`、`asset_name`、`state_instance_id`、`state`、`kind`、`severity`、`title`、`summary`、`summary_en`、`rule`、`evidence`、`occurred_at`、`session_id`、`locator`；ADR-24 的修复验证条目另带 `watch_id`、`signature`，资产字段为空，`id` 为负 watch id） |
| `/search` | `q`、`limit` | `assets`、`sessions`、`projects`、`programs`、`friction_categories`、`q` |

`/timeline` 的排序是 `occurred_at DESC, row_id DESC, kind`——三张表各自发号，所以 `kind` 是最后一道 tiebreak，
顺序因此是全序的，翻页不会漏行也不会重复。

**容易误读的地方**：*"通知可以自己产生或删除"*——不能（ADR-5）。通知是状态迁移的投影：
迁移在，通知就在；同一个状态实例内不会重复产生第二条。用户能做的是把某个实例"忽略"，那是一条 Disposition。

## 27.6 度量（`usage`，出现在会话行、`/overview`、`/projects/{key}`、`/stats`）

| 字段 | 含义 |
| --- | --- |
| `total_tokens` | **由分量重算**，不取源头的合计 |
| `input_tokens` | 非缓存输入 |
| `cached_input_tokens` / `cache_write_tokens` | 缓存读 / 缓存写 |
| `output_tokens` / `reasoning_tokens` | 输出 / 推理 |
| `lines_added` / `lines_removed` / `files_changed` | 代码改动量 |
| `active_ms` | 活跃时长（有下界：不会超过它所在会话的 `duration_ms`） |
| `known_sessions` / `token_sessions` / `cost_sessions` | 各项度量的**分母**：有多少个会话真的记了这一项 |
| `definition` / `definition_en` | 聚合端点上带一句口径说明，中英各一份 |
| `note` / `note_en` | 分母口径说明，中英各一份 |

不变量（`internal/api/consistency_test.go` 断言）：

```
total_tokens == input_tokens + cached_input_tokens + cache_write_tokens + output_tokens
active_ms    <= duration_ms                （逐会话）
```

**容易误读的地方**：*"`known_sessions` 小于总会话数说明有数据丢了"*——不是。
它是"记了这一项的会话数"这个分母本身：opencode 不写 token，它的会话就不进 `token_sessions`。
分母显式给出来，是为了让页面能写"114/114"而不是写一个孤零零的数。

## 27.7 资产与写入端点

| 端点 | 契约 |
| --- | --- |
| `GET /assets` | `view=wall` 给墙的投影（不含明细证据，共享的环境变化标记点被压到上限内）；不给 `view` 就是完整列表。`limit` 默认全量。响应 `{assets: [...]}` |
| `GET /assets/{id}` | `asset`、`description`、`versions`、`current_state`、`state_status`、`funnel`、`transitions`、`opportunities`、`participations`、`related_sessions`、`dispositions`、`reference_checks`。资产不存在 → 404 `asset not found` |
| `GET /assets/{id}/source` | 当前源文件内容预览 + 内容哈希（预览被截断时哈希仍按整份文件算） |
| `POST /assets/{id}/dispositions` | `{action, state_instance_id, confirmed, reason, rollback}`。**`confirmed` 不为 true 一律拒绝**（AGENTS.md §3） |
| `POST /assets/{id}/restore` | `{confirmed}`；返回 `{asset_id, restored: true}` |
| `GET /cleanup` | `{candidates: [{asset, reason, rollback, state_instance_id}]}` |
| `POST /cleanup` | `{asset_ids, confirmed, reason}`；`confirmed` 缺失或 `asset_ids` 为空 → 400。返回 `{archived: [...], source_files_changed: false}` |

**`source_files_changed: false` 是契约，不是这次碰巧。** 清理是逻辑归档：写的是 `dispositions` 与 `vital_states`，
外加一条可回滚记录；资产源文件一个字节都不动。

`related_sessions` 列的是**为这个资产制造过机会**的全部会话，包含没有记录到参与的那些——
机会是"相关任务"的分母，把它过滤成只剩参与，等于让没参与的那些任务记录凭空消失。

## 27.8 已知的契约毛边（记录在案，不是设计）

1. **`__unrecorded__` 一键两义。** `session_commands.program` 为空的 343 条，与"源头压根没记命令"共用这一个键。
2. ~~**`coverage_gaps` 不分项目。**~~ 已由 ADR-21 轮消除：缺口按（签名 × 项目）列出，
   "适用的规则" = 用户级资产 + 项目目录下的资产（见 `internal/api/coverage.go`）。

（原先记在这里的三条——`/friction` 不认 `from=all`、`/overview?from=30d` 返回 400、`coverage_gaps` 不分项目——已分别由 §27.10 与 ADR-21 轮消除。）

## 27.9 2026-08-23 收口时删掉的两个分支

| 删掉的 | 为什么 |
| --- | --- |
| `GET /api/v1/assets?summary=1` | 前端已经不再请求（现在只用 `view=wall`）。相关的 `assetSummaryItem` / `listAssetSummaries` 一并删除 |
| `GET /api/v1/sessions?summary=1` | 同上，前端零引用。`writeSessionSummaries` / `sessionSummaryResponse` 一并删除 |

两者都不带任何参数时的默认行为**没有变**：`/assets` 仍然返回完整列表，`/sessions` 仍然返回分页列表。


## 27.10 时间窗口：一个读法，七个端点（A9）

**谁做什么。** `/overview`、`/projects/{key}`、`/friction`、`/sessions`（含 `/sessions/facets`、`/sessions/export`）、
`/stats/time`、`/tools`、`/timeline` 收到 `from` / `to` 之后，都调同一个函数
（`internal/api/api.go` 的 `rangeBound`）把它读成一个存储形态的时间戳，或者读成"不设这一端"。
在这之前每个端点各写各的：会话端点认 `from=all`，摩擦端点把 `all` 当字面时间戳去比（于是返回空集），
总览只认绝对日期（于是 `from=30d` 是 400），时间线压根不收窗口。

| 你写的 | 读成什么 |
| --- | --- |
| 空 / `all` | 这一端不设界 |
| `2026-08-23` | 那一天的 `00:00:00.000Z`；作为 `to` 时是那一天的 `23:59:59.999Z`（整天在内） |
| `2026-08-23T07:15:00Z`（RFC3339，可带时区偏移） | 该时刻，转成 UTC |
| `7d` / `30d` / `90d` / `12w` / `6m` | 从**请求那一刻**往回数这么久；不按天取整 |
| 其它任何写法（`7y`、`0d`、`yesterday`、`2026-13-01`） | **400**，不静默当成"不设界" |

**什么在执行它。** `internal/api/api_test.go::TestRangeBoundReadsEveryAcceptedForm` 是这张表的表驱动测试；
`TestRangeWindowDefaultsToTheSameWindowAs30d` 钉住"总览不带 `from` = `from=30d`"；
`internal/api/friction_test.go::TestFrictionAPIReadsTheSameTimeWindowAsEveryOtherEndpoint`
在摩擦端点上断言 `from=all` 非空、且窗口越窄计数越小。

**容易误读的地方**：*"`from=30d` 和缺省窗口只是差不多"*——不是，它们是同一个窗口：
缺省下界就写成 `30d` 这个相对形式本身，两条路径走的是同一行代码。

**另一处容易误读**：*"相对窗口的响应缓存会过期"*——不会。响应缓存只认 `data_version`，
所以 `data_version` 不动的时候，`?from=7d` 复用的是上一次算出来的那个七天窗口。这与缺省窗口一直以来的行为相同。

## 27.11 daemon 写的解释：中英各一份（A9）

**谁做什么。** daemon 自己写下的每一句面向读者的解释——摩擦机制词典、分类规则、退出码语义、每个区块的口径 `note`、
度量的 `definition`——在响应里各多一个 `_en` 兄弟字段。中文字段的内容一个字没改。

| 中文字段 | 英文字段 | 出现在 |
| --- | --- | --- |
| `hint.mechanism` | `hint.mechanism_en` | `/friction` 分组行、`/overview.recurring_friction` |
| `coverage_gaps[].mechanism` | `coverage_gaps[].mechanism_en` | `/friction.summary` |
| `coverage_gap_note` | `coverage_gap_note_en` | `/friction.summary` |
| `category_rule` | `category_rule_en` | `/friction` 分组行与 `records[]` |
| `by_category[].rule` | `by_category[].rule_en` | `/friction.summary` |
| `scope.note` | `scope.note_en` | `/overview`、`/projects/{key}` |
| `parallelism.note` / `environment.note` / `subagents.note` / `reread.note` | 各自的 `note_en` | `/overview`、`/projects/{key}`（顶层与 `current`/`previous` 内各一份） |
| `usage.note` / `usage.definition` | `usage.note_en` / `usage.definition_en` | `/overview`、`/projects/{key}`、`/stats`、会话行 |
| `/sources` 的 `note` | `note_en` | `/sources`（GET 与 POST） |

**什么在执行它。** 机制与退出码是手写的封闭表，`internal/friction/hints_test.go::TestEveryHintRuleIsWrittenInBothLanguages`
与 `exitcodes_test.go::TestEveryExitRuleIsWrittenInBothLanguages` 断言表里每一条两种写法都非空——
只写一种语言的新规则会让测试变红。分类规则是逐条组装出来的句子（里面带匹配到的字面量或退出码），
`friction.Classify` 因此返回 `friction.Rule{Text, EN}`，两句由同一次匹配同时产出；
`internal/friction/friction_test.go` 断言凡是有类别的记录两句都非空。

**存法。** `category_rule_en` 是新列（迁移 `021_friction_category_rule_en.sql`），
`friction.ClassifierVersion` 随之推到 `friction/5`：带旧版本号的行在下一次启动时**从自己已存的 payload 重新分类**，
不重读任何源文件、不动任何事件。

**容易误读的地方**：*"`_en` 是把中文机翻了一遍"*——不是。两句话是同一条规则里并排写死的两个字符串，
技术术语（`exit_code`、`PATH`、`PreToolUse`、`old_string`、`__unparsed__`）两边都不翻译。

## 27.12 用户直接执行的命令（A9）

**谁做什么。** 用户在 Claude Code 里用 `!` 跑一条命令，harness 把它以 **user 角色**写进转写：
`<bash-input>…</bash-input>` 一条，输出跟在后面的 `<bash-stdout>` / `<bash-stderr>` 里。
没有人把这些当消息打出来，所以三者都**不计入 `user_message_count` / `message_count`**
（与 `<environment_context>`、`<task-notification>` 等注入块同一条口径，`canonical.InjectedMessagePrefixes`）。
但 `<bash-input>` 是"这条命令跑过"的唯一记录，所以它照旧留在转写里，并投影成一行命令。

| 记录 | 计入用户轮 | 计入 `tool_call_count` | 进 `session_commands` |
| --- | --- | --- | --- |
| `<bash-input>git push origin main</bash-input>` | 否 | 否（没有任何工具被调用） | 是：`tool_name='user_shell'`，`program` 照常解析，`exit_code` / `is_error` 为 NULL |
| `<bash-stdout>` / `<bash-stderr>` | 否 | 否 | 否 |

`exit_code` 为 NULL 不是"成功"，是**未记录**：Claude Code 不把退出码写进 `<bash-stdout>`。
因此这些行进 `/tools.programs[]` 与 `/overview.environment.failing_programs` 的 `calls`，
但**不进 `known_outcomes`**，也就不影响任何失败率的分子或分母。它们不出现在 `/tools.tools[]` 里——
`tools[]` 数的是工具调用，而这里没有工具被调用。

**什么在执行它。** `internal/runtime/session_hierarchy_test.go::TestUserRunCommandsAreCommandsAndNotTurns`
回放合成转写 `testdata/native/claude/cc-user-shell-fixture.jsonl`，断言用户轮 1、工具调用 0、命令 2、失败 0。
`eventstore.ProjectionVersion` 推到 `projection/6`：已入库会话在下一次启动时重投影，
`session_commands` 补上 `user_shell` 行、`session_stats` 同时按新口径重算计数（事件一行没动）。

**容易误读的地方**：*"不算用户轮 = 当作噪声丢掉"*——不是。丢掉的是 `<bash-stdout>` / `<bash-stderr>`
（与其它 harness 输出块同等对待）；`<bash-input>` 留着，而且比以前多了一行命令记录。

## 27.13 代价与机会（`GET /api/v1/insights`，ADR-20）

只读投影，无新表无写入；`from` / `to` 与其它端点同一读法，缺省窗口同总览（30d）。

响应：`{ range, scope, insights: [...], data_version }`。`insights` 只含有事实可报的条目，
空窗口返回空数组。每条：

| 字段 | 说明 |
| --- | --- |
| `kind` | 封闭六类：`interrupts` / `zero_edit_heavy` / `stuck_loops` / `reread` / `coverage_gaps` / `missing_commands` |
| `title` / `title_en` | 一行标题，中英各一份 |
| `summary` / `summary_en` | 一句话读数（数字来自 facts） |
| `criterion` / `criterion_en` | **判定规则原文**：入选条件、排序键、口径，一句话可解释（ADR-8） |
| `facts` | 结构化数字（每类形状不同），含分子/分母 |
| `links[]` | `href`（hash 路由下钻链接）+ `label` / `label_en` |

各类判定规则（也是 `criterion` 的内容）：

| kind | 规则 |
| --- | --- |
| `interrupts` | `friction_kind='user_interrupt'` 记录；"中断前工具"= 同会话时间不晚于该中断的最后一次 `transcript_tool_call`；等待集 = `exec_command` / `write_stdin` / `wait`；`turn_tokens_total`/`turn_measured` = 被中断轮次的 token 合计与可测数（仅消息级记录了 token 的来源，现为 Claude Code，见 ADR-22）；按次数排序 |
| `zero_edit_heavy` | 主会话、`total_tokens ≥ 5,000,000`、`lines_added+lines_removed=0`；改动行只统计编辑类工具，bash 改写不计入；按 token 降序取前 5 |
| `stuck_loops` | （签名, 会话）分组计数 ≥5；用户中断不算失败动作；按次数降序取前 5 |
| `reread` | 复用总览 reread 口径（同会话同路径 read ≥3 次） |
| `coverage_gaps` | 复用摩擦页 coverage 机制（反复出现的 harness 机制、规则资产正文未提关键词） |
| `missing_commands` | 复用总览环境区块口径（`command_not_found` 样例行或 `which` 探针） |

**容易误读的地方**：*"零改动 = 没有产出"*——不是。调研/分析会话本来就可能零改动，
而且 bash 改写（`sed -i` 等）不进改动行；页面陈述事实与口径，判断留给读者。
*"中断次数可以换算成浪费的 token"*——不可以。逐消息 usage 不在库里，估算即伪造。

## 27.14 规则闭环（ADR-21：简报与签名验证）

### `GET /api/v1/friction`（group=signature 时新增字段）

分组行新增：

| 字段 | 说明 |
| --- | --- |
| `brief` | 规则简报（见下），只读投影 |
| `watch` | 该签名当前验证的状态徽标（无验证时缺席） |

`brief`：`{mechanism, target, evidence, paste_prompt, paste_prompt_en, criterion, criterion_en}`。

- `mechanism`：提示字典的机制句（未覆盖为 `null`，简报如实标注）；
- `target`：`{kind, kind_label, reason, reason_en}`，按机制类别确定性映射：
  user_hook/permission→hook（拦截要强制）、environment→环境修复（写规则不解决）、
  user_interrupt→workflow（用户自己的动作，转工作流反思）、test/build→hook（收尾前强制跑）、
  其余→rule（行为指引）、字典未覆盖→unrecorded；
- `evidence`：`{count, session_count, project_count, first_seen_at, last_seen_at, sample_lines[≤3], top_projects[≤3]}`，
  样例是最近记录的原文截断（payload 无输出行时回退到所在会话标题，标注"（所在会话）"）；
- `paste_prompt` / `paste_prompt_en`：可直接粘贴给用户自己 agent 的起草提示词——Flatline 不写规则、不调模型。

### `GET /api/v1/signature-watches`

`{watches: [...], data_version}`。每条：`{id, signature, created_at, window_days, baseline_count,
baseline_session_count, project_keys, status, note, evaluation, criterion, criterion_en}`。

`evaluation`：`{post_count（创建后发生次数，判定读这个）, window_count（最近窗口发生次数，仅参考）,
project_sessions_in_window, status, resolved_at}`。

状态判定（`criterion` 原文随响应携带）：`verified` = 创建已满 window_days 天、创建后零发生、
且同项目窗口内确有会话在跑；`no_change` = 创建后仍发生；`unobservable` = 窗口内同项目无会话；
`watching` = 未满一个窗口。读取时评估并把结果写回 watch 行（只动 watch 表，不动事实层）；
verified 后再发生会翻回 `no_change`——闭环永不永久关闭，事实说了算。

### `POST /api/v1/signature-watches`

`{signature, confirmed, window_days?(1–90, 默认 14), note?}`。**`confirmed` 不为 true 一律 400**（AGENTS.md §3）。
创建时冻结基线（当时次数/会话数）与项目列表。只写本地库，不碰任何转写文件。

### `POST /api/v1/signature-watches/cancel`

`{id, confirmed}`。取消不删行：`status='cancelled'`，尝试过的验证历史保持可审计。未确认 400；不存在/已取消 404。
