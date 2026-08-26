# DEVELOPMENT · Flatline 开发指南

> 本文件规定 Flatline 的构建、测试、质量门禁与目录约定。
> P1 骨架已落地；以下门禁与目录约定从现在起适用于实现代码。

## 1. 技术栈（目标）

| 层 | 选型 | 约束 |
| --- | --- | --- |
| 语言 | Go（稳定版，≥ 1.22） | 单二进制交付 |
| 存储 | SQLite，**纯 Go 驱动**（如 `modernc.org/sqlite`） | 禁止 CGO 依赖，保证交叉编译 |
| 运行形态 | 单 daemon，唯一数据属主 | 无多进程写同一 DB |
| API | 本地 HTTP，**仅绑定 loopback**（127.0.0.1） | 禁止 0.0.0.0 绑定，禁止外网依赖 |
| 前端 | SPA，构建产物经 `go:embed` 内嵌 | 构建产物不入库（`web/dist/` 已 gitignore） |
| 迁移 | `migrations/` 目录，编号 SQL 文件 | 只增不改（append-only），变更需新文件 |

## 2. 目录约定

```
cmd/                    # daemon 与 CLI 入口（main 包）
internal/               # 全部业务代码（不可被外部 import）
internal/web/static/    # SPA 本体（手写 ES module + CSS，go:embed 直接内嵌，无构建步骤）
migrations/             # SQLite schema 迁移（NNN_描述.sql，append-only）
web/                    # 保留占位；SPA 实际在 internal/web/static/
scripts/                # 构建/检查/回测脚本
testdata/               # 合成 fixture（禁止真实用户数据）
docs/                   # 设计文档、roadmap、ADR
docs/qa/                # 真实使用记录与 UI 截图（dogfood）
```

- 业务代码一律放 `internal/`，`cmd/` 只做装配；
- 新增顶层目录需 ADR 或 roadmap 依据。

`internal/api` 一个文件一个领域：`api.go` 只留 `Server`、路由表和跨领域 helper；
`assets.go` / `assetfacts.go` / `sessions.go` / `friction.go` / `overview.go` / `period.go` / `projects.go` /
`timeline.go` / `notifications.go` / `stats.go` / `cleanup.go` / `ingest.go` / `events.go` … 各管一段。
测试文件按同一套领域命名（`assets_test.go` 对 `assets.go`），共用的 fixture 与 HTTP 助手在 `helpers_test.go`。
**不要按工作线命名文件**（`a7_test.go` 这类）——线是临时的，领域不是。

## 3. Go 质量门禁

每次提交/PR 必须通过：

```bash
gofmt -l .            # 输出必须为空
go vet ./...           # 必须通过
go test ./...          # 必须全绿
```

- 测试与源码同包（`_test.go`）；表驱动测试优先；
- 涉及 detector/状态机的测试必须基于 `testdata/` 中的合成 fixture 回放，fixture 变更需说明；
- 派生层（detector、状态机）必须携带 detector/schema 版本，支持全量重算（ADR-10）；
- 禁止在测试中访问网络；禁止测试写真实资产目录。

## 4. 前端质量门禁

SPA 是 `internal/web/static/` 下手写的 ES module 与 CSS，**没有 npm、没有构建步骤**：
`go:embed` 直接内嵌源文件，`go build` 就是全部的前端构建。因此前端门禁走 Go 测试：

```bash
go test ./internal/web    # 断言内嵌资源存在、静态文件带 ETag 且改动后失效、页面契约字段仍在
```

- 改了 `app.js` / `style.css` 之后必须重建二进制才能在页面上看到——`internal/web/web_test.go`
  里的 `TestEmbeddedSPARevalidatesStaticFilesAfterRebuild` 守的就是"换了二进制浏览器还在用旧 JS"这个坑；
- 组件必须实现 UI 指南 §9 组件清单的规格（含观测等级角标、分子分母展示）；
- 文案规则（UI 指南 §3.3）：只写对齐不写因果、比例带分子分母、unknown 有明确文案；
- 颜色语义遵循 UI 指南 §8（颜色必须与文字/形状共同表达）。

## 5. 测试分层

| 层 | 范围 | 工具/位置 | 要求 |
| --- | --- | --- | --- |
| 单元 | 纯函数：阈值判定、基线计算、形状归类、引用提取 | `*_test.go` | 每个 detector 判定规则 100% 覆盖边界（如机会数 = N-1 / N / N+1） |
| 集成 | 模块协作：adapter → event store → tracker → 状态机 | `internal/**/integration_test.go` | 基于 `testdata/` fixture 全链路回放 |
| 回测 | 状态机在 ≥3 个月合成历史上回放（真实历史回测为发布验收，见 roadmap P7） | `scripts/backtest` | 判定准确率人工核验，结果可复现 |
| 跨端点一致性 | 同一个数在不同端点上是不是同一句话 | `internal/api/consistency_test.go` | 会话数七处相等、token 分量自洽、`active_ms ≤ duration_ms` |
| 前端 | 内嵌资源、静态文件缓存失效、页面契约字段 | `internal/web/web_test.go` | 覆盖 canonical 观测等级（含 `unknown`）与"未记录"态 |
| 端到端 | daemon 启动 → loopback API → SPA 黄金路径 | 无头 Chrome（CDP），见 §9 第 4 步 | 6 步黄金路径 + 清理路径可走通，控制台无报错 |
| 原文对账 | API 度量 vs 原始转写 | `scripts/audit_accuracy.py`，见 §9 第 2 步 | 每个 harness 抽 8 个会话，0 mismatching fields |

原则：

- 小任务先测试（AGENTS.md §4）：先写失败测试，再实现；
- 测试失败先怀疑实现，不修改断言迁就错误行为（除非断言本身错，需说明）；
- fixture 必须合成且自描述（文件名/头部注释说明场景与预期判定）。

## 6. 迁移规范

- 文件命名：`NNN_短描述.sql`（三位编号递增，如 `001_initial.sql`）；
- 只增不改：已合入的迁移文件永不修改，schema 变更一律新文件；
- 每个迁移附：前向 SQL + 回滚说明（或显式声明不可回滚及理由）；
- 派生表（opportunities、participations、vital_states 等）必须可整体重算——阈值调整后全量回放状态历史必须是廉价操作（ADR-10）；
- schema 变更需同步更新系统设计 §7 的对象模型表。

### 6.1 迁移是 append-only，由测试强制

**谁做什么。** `migrations/lock_test.go` 里存着每个已发布迁移文件的 SHA-256。
`go test ./migrations` 重算一遍并比对。

| 你做了什么 | 测试的反应 | 你该做什么 |
| --- | --- | --- |
| 新增 `NNN_*.sql` 并把哈希加进 `releasedMigrations` | 绿 | 正常流程 |
| 改了一个已发布的 `.sql` | 红：`TestReleasedMigrationsAreNeverEdited` | **把改动拆成新文件**，把原文件改回去 |
| 新增文件但忘了登记哈希 | 红 | 补登记 |

**为什么不能改。** 迁移一旦被任何一个数据库记进 `schema_migrations` 就不会再跑第二遍。
改文件只影响此后新建的库；已经存在的库停在旧 schema 上，daemon 会在一个"有一半安装缺失"的列上崩。

**容易误读的地方**：*"哈希只是防手滑"*——不是。它是这条纪律唯一的执行者；
`go test ./migrations` 是红的，就说明有一批已有数据库会拿到和代码不一致的 schema。

### 6.2 两个迁移通道（写在 SQL 第一行的标记）

普通迁移整份跑在一个事务里。两种情况不行，各有一个显式标记：

| 标记（必须是文件第一行） | 运行器怎么跑 | 什么时候用 | 代价 |
| --- | --- | --- | --- |
| `-- flatline:no-transaction` | 事务外执行，`foreign_keys` 关闭，跑完检查没有悬挂引用才登记 | **重建一张表**（`CREATE new / INSERT SELECT / DROP old / RENAME`）。事务内的 `PRAGMA foreign_keys=OFF` 是空操作，`DROP TABLE` 的隐式 `DELETE` 会顺着 `ON DELETE CASCADE` 把子表清空 | 中途失败没有回滚，只能靠新迁移修 |
| `-- flatline:tolerate-existing` | 逐条执行，只吞掉 `duplicate column name` 这一种错误 | 把某个已发布迁移的**后加列**拆成新文件时——新文件在早期库上要真加，在后期库上必须是空操作 | 只忽略这一种错误，别的照样红 |

**容易误读的地方**：*"`tolerate-existing` 是容错开关"*——不是。
它只吞 `duplicate column name`。任何别的错误仍然让迁移失败，迁移也不会被登记。

**`writable_schema` 不是第三个通道。** 迁移 012 用 `PRAGMA writable_schema` 原地摘掉一个 CHECK，
是因为去掉 CHECK 不改变磁盘行格式；表重建走 `no-transaction`。
`migrations/open_sources_test.go` 用真实数据回放 012，断言 events / session_stats / session_tags / session_annotations 一行没少。

## 7. 构建与运行（目标形态）

```bash
# 构建单二进制（内嵌 SPA）
go build -o bin/flatline ./cmd/flatline

# 运行 daemon（loopback）
./bin/flatline daemon   # 监听 127.0.0.1:<port>

# CLI
./bin/flatline status   # 资产状态摘要
```

- 数据文件（`*.db`）位于用户本地数据目录，不入库、不上传；
- 首次运行执行历史回测，生命体征墙即刻点亮（验收标准 3）。

## 8. 隐私边界（开发红线）

- 不引入任何上传/遥测/第三方网络请求的依赖；
- 不引入账户/登录/云同步相关代码（MVP 明确不做）；
- `testdata/` 与 fixture 禁止包含真实用户 Session 数据；
- 本地 API 仅 loopback 绑定，代码审查时确认无 0.0.0.0/外网监听。

## 9. 每次构建后的核对流程

自动化测试只证明代码自洽。这四步证明**输出和现实一致**——统计准确性是这个产品成立的前提，
所以它们不是"有空再跑"，是每次构建后都要跑一遍。

### 第 1 步 · 静态门禁

```bash
scripts/check.sh          # gofmt -l . 为空、go vet ./...、go test ./...
```

三者任一不过就停在这里，后面三步没有意义。

### 第 2 步 · 原文对账（API 说的和转写里写的一致吗）

先在一个可丢弃的库上起一个 daemon（端口自选，`127.0.0.1` 只绑回环）：

```bash
export PATH=$HOME/.local/go/bin:$PATH
CGO_ENABLED=0 go build -o bin/flatline ./cmd/flatline
setsid nohup ./bin/flatline daemon -listen 127.0.0.1:8808 -db /tmp/check.db > /tmp/check.log 2>&1 &
# 等到 /api/v1/ingest/status 的 status 变成 "ready"
python3 scripts/audit_accuracy.py http://127.0.0.1:8808 8 claude_code
python3 scripts/audit_accuracy.py http://127.0.0.1:8808 8 codex
```

**这个脚本做什么。** 随机抽 N 个会话，打开它们的原始转写文件自己数一遍，
再把 `user_message_count` / `tool_call_count` / `tool_result_count` / `usage.output_tokens` 和 API 的答案逐字段比。

| 输出 | 意思 | 谁来处理 |
| --- | --- | --- |
| `0 mismatching fields` | 这一批会话的度量和原文一致 | 通过 |
| `[DIFF] … key=X≠raw Y` | 解析口径和原文不一致 | **改解析，不改断言**；改完要把 `history.ParserVersion` 往上推一位，让全部转写重读 |
| `[skip] … raw file not found` | 抽到的会话原始文件已不在本机 | 不算失败，但连续大量出现说明源根配错了 |

**容易误读的地方**：*"随机 8 个过了就是全对"*——不是。它是抽样，不是全量证明。
它能抓住的是**口径级**的错（一类会话全错），抓不住单个会话的偶发问题。

### 第 3 步 · 一致性等式（几个页面对同一个数说的是不是同一句话）

**每个端点的筛选都必须开到全量**，否则比的不是同一批会话：

```bash
B=http://127.0.0.1:8808/api/v1
curl -s "$B/ingest/health"                                   # counts.sessions          ← 基准
curl -s "$B/overview?from=all&include=all"                   # sessions.total / in_range
curl -s "$B/sessions/facets?thread=all&empty=all&from=all"   # total
curl -s "$B/sessions?thread=all&empty=all&from=all&limit=1"  # pagination.total
curl -s "$B/stats"                                           # session_count
curl -s "$B/projects"                                        # Σ projects[].sessions
```

`health.counts.sessions` 是整张表的行数，其余六个读数把筛选打开后都必须落在同一个数上。
同一组等式由 `internal/api/consistency_test.go::TestEveryEndpointCountsSessionsTheSameWay` 在 CI 里
用 fixture 断言，第 3 步是在**真实库**上复验一遍。

**容易误读的地方**：*"`overview.sessions.in_range` 和 `health.counts.sessions` 对不上就是 bug"*——不一定。
不带 `include=all` 时总览的默认口径是"主会话且非空"（`scope.key = "main_non_empty"`），
`/sessions` 与 `/sessions/facets` 的默认口径也是这个。本机默认口径 472、全量 1167，
两个数都对，只是回答的不是同一个问题。**先把口径对齐再比**。

同一份文件还断言另外两条：`total_tokens == input + cached_input + cache_write + output`（token 分量自洽），
以及 `active_ms ≤ duration_ms`（活跃时长不能超过它所在的会话）。

### 第 4 步 · CDP 自检（页面真的渲染出来了吗）

用无头 Chrome 打开 loopback 地址，逐页截图，并检查控制台没有报错。
后端改动也要走这一步：**返回 200 不等于页面画得出来**——字段改名、数组变对象都能让页面在不报 HTTP 错的情况下空掉。

### 收口类改动额外一步 · 行为不变的证明

重构（只移动/合并/删除，不改行为）要拿出证据，而不是声称：
**同一份数据库**，改动前后各起一次 daemon，对同一组端点抓响应 JSON，
去掉三类天生会变的字段后 diff 必须为空。

| 字段 | 为什么天生会变 |
| --- | --- |
| `data_version` | 每次 daemon 启动都会推进 |
| `last_import.started_at` / `finished_at` | 本次进程的导入时钟 |
| `wal_bytes` | SQLite WAL 当前大小 |

其余任何一处不等，就是行为变了——不管改动看起来多"纯粹"。

## 10. 交付检查（与 CONTRIBUTING 审查清单一致）

提交前自查：

- [ ] `scripts/check.sh` 全绿（前端：lint/test/build）；
- [ ] `scripts/audit_accuracy.py` 两个 harness 各 8 个会话，0 mismatching fields；
- [ ] 一致性等式在真实库上成立；
- [ ] CDP 自检逐页无控制台报错；
- [ ] 无伪造数据、无因果结论、无不透明分数；
- [ ] 资产写入/删除路径有显式确认与可回滚记录；
- [ ] 迁移 append-only（`go test ./migrations` 绿），附回滚说明；
- [ ] 文档同步（设计文档 §27 API 契约 / roadmap / ADR）；
- [ ] 无新增网络依赖、无遥测、无 MVP 范围外功能。