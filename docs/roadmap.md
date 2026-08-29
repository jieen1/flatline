# Flatline · 路线图（Roadmap）

> 版本：v1.1 · 2026-08-19 立，2026-08-23 补记 P8–P15。
> 依据：`flatline-system-design-v0_4.md`（系统设计）、`flatline-ui-design-guidelines-v2_0.md`（UI 指南）、
> `flatline-session-first-redesign-v1.md`（会话优先重构，当前 API 契约见其 §27）。
> 产品定位：**本地优先的 Agent 工作历史监护仪**——主线是会话管理与摩擦发现，资产生命体征是下钻层（ADR-18）。
> 目标架构：**Go + 纯 Go SQLite 驱动 + 单 daemon + loopback + 内嵌 SPA**。
> MVP **不包含**：账户/云同步、AI 分析、桌面壳、群体参考、插件运行时、正式统计改善轮次（ImprovementCycle，整体移入 Backlog，UI 与代码均不实现）。

## 阶段总览

| 阶段 | 名称 | 一句话目标 | 允许并行 |
| --- | --- | --- | --- |
| P0 | 基线 | 仓库、规范、文档就位 | 是（文档类） |
| P1 | 可运行骨架与 SQLite schema | daemon 能启动、loopback API 能响应、schema 可迁移 | 部分（前端脚手架可并行） |
| P2 | 事实层与 Source Adapters | CC + Codex 会话可被摄取为 canonical 事件 | 是（CC/Codex 适配器可并行） |
| P3 | 资产快照/机会/参与/基线 | 每个资产有版本、基线与参与记录 | 部分（快照器与 tracker 有依赖） |
| P4 | 四 detector 与状态机 | 状态判定与迁移事件可回放 | 是（四个 detector 可并行，状态机串行收口） |
| P5 | API 与内嵌 SPA 生命体征墙 | 墙 + 诊断页 + 时间线可交互 | 是（API 与前端组件可并行） |
| P6 | 诊断/时间线/处置/修改后验证/清理 | 四拍核心流程全链路走通 | 部分（处置与修改后验证有依赖） |
| P7 | 回测与发布 | 真实历史回测验收 + 单二进制发布 | 否（发布串行） |
| P8–P15 | 2026-08 会话优先重构 | 会话读准、摩擦看得出还在不在发生、五源覆盖、后端收口 | 见本文件末节 |

P0–P7 是**资产监护**这条链路的分期，写于 2026-08-19，状态原样保留。
P8–P15 是 2026-08-22/23 实际发生的第二条主线，记在本文件末尾的"2026-08 会话优先重构"一节。

---

## P0 · 基线

**目标**：仓库与协作规范就位，所有后续工作有明确的文档依据与质量门禁。

**交付物**：
- 根目录：`.gitignore`、`.editorconfig`、`AGENTS.md`、`README.md`、`CONTRIBUTING.md`、`DEVELOPMENT.md`；
- `docs/roadmap.md`（本文件）、`docs/adr/README.md`；
- 目录骨架：`cmd/`、`internal/`、`migrations/`、`web/`、`scripts/`、`testdata/`（含 `.gitkeep`）；
- 原型 zip 保留于 `docs/`（只读设计输入，不解压到生产目录）。

**验收标准**：
- [x] 文档齐备且互相引用一致（README ↔ roadmap ↔ 设计文档）；
- [x] AGENTS.md 的证据纪律、写入纪律、交付规范完整；
- [x] P0 完成时尚无 `go.mod`、无依赖、无业务代码（P0 不引入）；
- [x] 原型 zip 未被解压到 `web/` 等生产目录。

**依赖**：无。
**允许并行**：是（各文档独立）。

---

## P1 · 可运行骨架与 SQLite schema

**目标**：最小可运行形态——daemon 进程能启动、loopback API 能响应健康检查、SQLite schema 可前向迁移。

**交付物**：
- `go.mod`（Go 模块初始化，引入纯 Go SQLite 驱动，如 `modernc.org/sqlite`）；
- `cmd/flatline`：daemon 入口（`daemon` 子命令），仅绑定 127.0.0.1；
- `internal/` 骨架：配置、存储（DB 打开/迁移执行）、API 装配；
- `migrations/001_initial.sql`：v0.4 对象模型全表（assets、asset_versions、sessions、events、effective_bundles、opportunities、participations、vital_states、state_transitions、dispositions、reference_checks；**不含** decision_tasks 与 improvement_cycles 系列）；
- ADR 正式文件：`docs/adr/001-*.md` 至 `010-*.md`，将 ADR-1 至 ADR-10 文件化（索引与流程见 `docs/adr/README.md`）；
- 健康检查端点（`GET /healthz`）；
- `scripts/`：构建与检查脚本（fmt/vet/test）。

**验收标准**：
- [x] `go build` 产出单二进制，`./bin/flatline daemon` 启动后仅监听 loopback；
- [x] 迁移可前向应用，附回滚说明；重复执行幂等；
- [x] `gofmt`/`go vet`/`go test` 全绿；
- [x] 无网络依赖、无遥测、无账户相关代码。

**依赖**：P0。
**允许并行**：部分——前端脚手架（`web/` 初始化、lint/test/build 管线）可与 Go 骨架并行；schema 设计需串行评审。

---

## P2 · 事实层与 Source Adapters

**目标**：CC 与 Codex 的真实会话历史可被增量摄取为 canonical 事件（append-only + locator + EnvironmentChanged 锚点）。

**交付物**：
- Source Adapters 框架：字段矩阵、fixture 规范、版本探测（ADR-3：自建 canonical schema，AgentView 类工具为解析参考/兼容输入）；
- CC 适配器：显式调用（invoked，Exact）、内容哈希、版本字段、引用体检输入；
- Codex 适配器：同字段矩阵覆盖；
- Canonical Event Store：append-only 写入、locator 关联、EnvironmentChanged 锚点（Harness 版本/模型变化）；
- `testdata/` 合成 fixture（每适配器 ≥ 3 个场景：正常、版本变化、缺失字段）；
- `observation_level` 随事件携带（封闭枚举：invoked / observed-use / loaded / offered / inferred / unknown，定义见系统设计 §3.1）；参与形式与观测等级分开记录。

**验收标准**：
- [x] fixture 回放：事件写入后 locator 可下钻到原始来源位置；
- [x] 缺失字段如实标注（缺失 ≠ 零），不伪造；
- [x] 增量摄取幂等（重复摄取不产生重复事件）；
- [x] 每个适配器有字段矩阵文档（支持/不支持/未记录）；
- [x] 单元测试覆盖版本探测与字段映射边界。

**依赖**：P1（存储与迁移）。
**允许并行**：是——CC 与 Codex 适配器可并行开发（共享框架接口先行冻结）。

---

## P3 · 资产快照/机会/参与/基线

**目标**：每个资产有版本快照与内容哈希；每个 Session 有任务形状归类；参与记录分观测等级；基线滚动计算。

**当前进度**：核心注册、只读文件快照入口、版本幂等/观测升级、EffectiveBundle、机会/参与追踪与基线已实现；自动目录发现和 daemon 摄取编排留待后续收口。

**交付物**：
- Asset Snapshotter：资产发现（Skill/AGENTS.md/Rule/Hook）、内容哈希、版本切分、观测升级器（快照跃迁规则）；
- Asset Registry：资产/版本注册与查询；
- Effective Bundle Resolver：版本向量强制关联（Session ↔ 当时生效的资产版本集）；
- Opportunity & Participation Tracker：任务形状归类、机会计数、参与记录（分观测等级）、基线滚动窗口计算（参与率 + 绝对次数）；
- 基线可解释：分子/分母/窗口/阈值版本可查询。

**验收标准**：
- [x] 资产修改后产生新版本，EffectiveBundle 可回答"某 Session 当时生效的是哪个版本"；
- [x] 基线计算在合成 fixture 上可人工核验（参与率与绝对次数均正确）；
- [x] 参与记录全部携带观测等级；loaded/offered 仅在数据源支持时记录并如实标注；
- [x] 形状归类规则文档化（什么算"同类任务"），归类结果可下钻到判定依据；
- [x] 派生层携带 detector/schema 版本，支持整体重算。

**依赖**：P2（canonical 事件与资产输入）。
**允许并行**：部分——Snapshotter/Registry 与 Opportunity/Participation Tracker 可并行（接口先行）；EffectiveBundle Resolver 依赖两者。

---

## P4 · 四 detector 与状态机

**目标**：四个 detector 产出判定，Vital State Machine 作为唯一状态裁决者输出状态与迁移事件；全链路可回放。

**交付物**：
- Detector 1 沉默判定：有基线（历史参与 ≥5 次且参与率 ≥30%）且连续 ≥N 个机会（默认 8）零参与；
- Detector 2 引用体检（Reference Health Checker）：从资产内容提取命令/路径/工具引用，对本机环境体检；随资产版本变化与定时触发；
- Detector 3 调用后违背（Bypass Detector）：invoked-then-violated（CC 侧证据链两端 Exact）；
- Detector 4 休眠识别：存在 ≥30 天且累计参与 ≤2 次；
- Vital State Machine：十态状态机（healthy/degraded/silent/broken/bypassed/dormant/no_opportunity/unobservable/awaiting_resurrection/archived），阈值集中配置，输出 StateTransition（含 ±3 天对齐变化列表）；
- 判定依据快照写入 `vital_states`（分子/分母/基线/阈值版本）。

**验收标准**：
- [ ] 每个 detector 的判定规则满足 ADR-8：一行分子分母说得清；
- [ ] 状态机迁移表与设计文档 §3.3 完全一致（含 broken 叠加规则）；
- [ ] 同一资产任意时刻恰好一个主状态；
- [ ] 阈值调整后全量回放状态历史为廉价操作（ADR-10），回放结果确定性可复现；
- [ ] 边界测试全覆盖（机会数 N-1/N/N+1、参与率临界、30 天临界）；
- [ ] 无因果结论：迁移事件只含对齐列表，不含"导致"。

**依赖**：P3（tracker 与基线输入）。
**允许并行**：是——四个 detector 可并行开发（输入接口在 P3 冻结）；状态机串行收口，最后统一回放验证。

---

## P5 · API 与内嵌 SPA 生命体征墙

**目标**：本地 API 暴露状态数据，内嵌 SPA 呈现生命体征墙（首页）、会话页、变化时间线三页骨架。

**交付物**：
- Local API：资产列表/详情、状态与迁移、机会与参与、Session 浏览、时间线查询（仅 loopback）；
- SPA 脚手架（`web/`）：三页信息架构（生命体征 / 会话 / 变化时间线），路由 `/assets/<id>` 诊断下钻；
- 核心组件（UI 指南 §9）：Asset Vital Row、Participation Sparkline（含双类变化标记点）、State Badge、Verdict Card、Alignment List、Provenance Badge（观测等级角标）；
- 其余诊断/处置组件（Cause Candidate、Funnel Compare、Reference Check Report、Prune Sheet、Verification Toast、Diff Viewer、Evidence Block、Session Reference、Timeline Track）在 P6 完成；
- 墙的分区结构：需要注意 / 观察中 / 健康 / 休眠（折叠+清理入口）/ 没有相关任务记录·不可观测·已归档（折叠灰度）；
- `go:embed` 内嵌构建产物，单二进制交付。

**验收标准**：
- [ ] 单二进制启动后浏览器打开 loopback 地址即可使用，无外部资源请求；
- [ ] 只看首页能回答"哪些资产死了、哪些在耗上下文"；
- [ ] 每个沉默判定可见分子、分母、基线和机会定义原文；
- [ ] 每条 sparkline 有变化标记点（资产版本=实心、环境变化=空心）；
- [ ] 无"待办数"、无健康分排行、无因果句式、无空白式 unknown；
- [ ] 前端 lint/test/build 全绿；"没有相关任务记录"与"不可观测"有独立灰度表达。

**依赖**：P4（状态与迁移数据）。
**允许并行**：是——API 端点与前端组件可并行（API 契约先行冻结）；时间线页与墙可并行。

---

## P6 · 诊断/时间线/处置/修改后验证/清理

**目标**：四拍核心流程（监护 → 告警 → 诊断 → 处置 → 修改后验证）全链路走通，批量清理流可用。

**交付物**：
- 诊断页完整实现：判定依据卡、候选原因（证据前置，含"未知原因"诚实示例）、参与漏斗现在 vs 基线、处置历史；
- 四种处置：修改（Diff/外部编辑器，保存后进入 awaiting_resurrection）、清理/删除（确定性收益展示 + 显式确认 + 可回滚记录，不物理删除）、归档（停止监护可恢复）、忽略（持久化为 Disposition，当前状态实例内静默，见系统设计 §4.3）；
- 修改后验证：awaiting_resurrection 期间首次重新参与且未违背 → healthy + 修改后验证通过通知（直达 Session 调用位置）；N 个机会仍零参与 → 修改后验证未通过通知；
- 批量清理流：休眠资产表格、合计上下文占用、批量归档/保留、移除清单生成、一键回滚；
- 变化时间线页完整实现：三类事件同轴、按资产过滤、聚簇提示（陈述句）；
- 通知体系：问题/修改后验证通知仅由状态迁移产生、同状态零重复、通知自带判定依据原文；休眠月度汇总是非告警摘要，不改变状态迁移规则。

**验收标准**：
- [ ] 从沉默告警到完成一次修改并收到修改后验证通过通知，全流程无需离开产品（外部编辑器除外）；
- [ ] 批量清理给出确定性收益数字并生成可执行清单，可回滚；
- [ ] 所有资产写入/删除操作有显式确认（AGENTS.md §3），无静默修改；
- [ ] 候选原因全部证据前置，存在"未知原因（无支持证据）"示例；
- [ ] 修改后验证通过通知一键直达那个 Session 的调用位置；
- [ ] 时间线页可演示"多个资产沉默聚簇在同一环境变化附近"（陈述句，不判因果）。

**依赖**：P5（API 与页面骨架）。
**允许并行**：部分——诊断页/时间线页/清理流可并行；处置与修改后验证共享状态机写入路径，需串行收口。

---

## P7 · 回测与发布

**目标**：在开发者 ≥3 个月真实历史上回放状态机完成回测验收，产出可发布的单二进制。

**交付物**：
- 回测工具（`scripts/backtest`）：真实历史回放 + 判定结果导出（人工核验用）；
- 回测报告：每个被判沉默/休眠/失效的资产人工核验记录；
- 发布物：单二进制（跨平台，纯 Go 无 CGO）、安装说明、首次使用流程（历史回测即刻点亮墙）；
- 文档定稿：README、设计文档、UI 指南、ADR 与实现一致性核对。

**验收标准**（对应系统设计 §9；2026-08-29 首次在 4 个月真实历史上回放，记录见 `docs/qa/backtest-2026-08-29.md`）：
- [ ] 回测正确性：**部分通过**——895 条迁移中 24 条非平凡判定逐条人工核验无误；
      四个告警态在真实历史上零触发，"报警准确"一半无案例可测，保持未勾（回测记录 §5）；
- [x] 任意告警可下钻到判定依据原文（以参与证据同机制验证：locator → 原始转写行，两条抽查零偏差）；
- [x] 首次导入完成后墙即刻点亮——**带缺陷记录**：首启引导期 12 个资产以"几乎未使用"语义
      可见约一个导入周期，实义是"历史未读入"（回测记录 §4，候选修法需 ADR）；
- [ ] 从沉默告警到修改后验证全流程：真实数据上 silent 从未发生，维持 fixture 背书；
- [ ] 批量清理收益：本轮 0 个清理对象，未复验；
- [x] 每个比例可见分子分母；loaded/invoked 如实分级、无相关任务记录不显示为零；
- [x] 全流程不依赖登录与上传；
- [ ] UI 指南 §12 原型验收清单逐项通过（6 步黄金路径 + 清理路径连续点击走通）。

**关于 `scripts/backtest` 交付物**：未另建。daemon 首次导入即全量评估（ADR-10）就是回放路径，
另写脚本是第二套口径；人工核验以本记录承担。

**依赖**：P6（全链路功能）。
**允许并行**：否——回测、发布物、文档定稿串行收口（回测发现判定问题需回退 P4 修复后重放）。

---

## 2026-08 会话优先重构（P8–P15）

> 补记于 2026-08-23。P0–P7 是**资产监护**这条链路的分期，状态与验收标准原样保留在上面，不改。
> 这一节记录 2026-08-22 至 08-23 之间实际发生的第二条主线：**先把会话历史读准，再从摩擦往资产下钻**（ADR-18）。
> 设计依据 `docs/flatline-session-first-redesign-v1.md`（当前 API 契约见该文档 §27 总表），
> 实测记录 `docs/qa/dogfood-2026-08-22.md`（十轮，全部基于本机真实历史，非 fixture）。

**这一节和 P0–P7 是什么关系。** P0–P7 没有被推翻，也没有被替换：四个 detector、状态机、处置与修改后验证仍然是资产层的实现，
只是它们的**入口**变了——用户从"哪条摩擦反复出现"或"这个会话里发生了什么"进去，落到资产页，而不是把资产墙当首页。
P7（三个月真实历史回测）仍然未完成，见下面"未完成"。

| 阶段 | 一句话目标 | 主要交付 | 状态 |
| --- | --- | --- | --- |
| P8 | 会话是一等对象 | 迁移 `006`；会话列表/检索/分面/详情/标注/导出；响应缓存与 ETag | 完成 |
| P9 | 会话层级与聚合页 | 迁移 `007`–`009`；主会话/子代理、命令与文件投影、项目页、时间统计、摩擦签名 | 完成 |
| P10 | 摩擦看得出"还在不在发生" | 迁移 `010`；工具身份配对、签名 v3、工具投影、展示名、摩擦生命周期 | 完成 |
| P11 | 会话度量与原文对得上 | 迁移 `011`–`015`；退出码语义、版本化重读、`session_usage`、子代理归属、注入块不算用户轮 | 完成 |
| P12 | 五个数据源 | 迁移 `012`/`016`；opencode / dsh / Hermes 适配器、`normalized` 适配器框架、ADR-19 | 完成 |
| P13 | 总览 = "这段时间发生了什么" | 迁移 `016`–`019`；`current`/`previous`/`delta`、并行度/环境/子代理/重复读取、token 口径统一、`sources` 注册表 | 完成 |
| P14 | 准确性残留 + 摩擦→资产桥 | 迁移 `020`；程序名解析、`which` 探针、opportunity 作废通道、hook 证据桥、规则覆盖缺口 | 完成 |
| P15 | 后端收口（不加功能） | `internal/api` 按领域拆分、helper 去重、死代码清除、测试按领域重排、文档与现实对齐 | 完成 |

### 验收证据（全部来自本机真实历史，不是 fixture）

以下每条都能在 `docs/qa/dogfood-2026-08-22.md` 里找到原始记录。

| 门禁 | 怎么验 | 最近一次结果 |
| --- | --- | --- |
| **原文对账** | `scripts/audit_accuracy.py <api> 8 claude_code` 与 `... 8 codex` 随机抽会话，把 API 的 `user_message_count` / `tool_call_count` / `tool_result_count` / `usage.output_tokens` 与原始转写逐字段比对 | 8+8 会话，**0 mismatching fields**（第九轮、第十轮、P15 收口后复测） |
| **一致性等式** | 把每个端点的筛选开到全量（`include=all` / `thread=all&empty=all&from=all`）后，`health.counts.sessions == overview.sessions.total == overview.sessions.in_range == facets.total == sessions.pagination.total == stats.session_count == Σprojects.sessions`；`internal/api/consistency_test.go` 在 CI 里断言同一组等式 | 第九轮 1164 全等，第九轮（合并后）1165 全等，P15 收口后 1167 全等 |
| **token 口径** | `total_tokens == input + cached_input + cache_write + output`，聚合端点带 `usage.definition` | 逐项相等（第九轮） |
| **不变量：`active_ms ≤ duration_ms`** | 全量扫描会话 | 修完软链重复读取与 `ended_at` 冻结后，本机 **0 例**越界（第九轮） |
| **页面耗时**（稳态、5 次、绕开响应缓存） | `curl` 直打端点 | `/overview?compare=1` 0.26–0.31 s；`/overview?from=all&compare=1` 0.34–0.37 s；项目页 compare 0.25 s；`/timeline?limit=1000` 0.07–0.08 s；`/stats` 0.27 s；冷启 1.33 s（第九轮） |
| **收口不改行为**（P15） | 同一份库、同一组端点，拆分前后各抓一次响应 JSON，去掉 `data_version` / `last_import` 时间戳 / `wal_bytes` 后 diff | 11 个端点全部逐字节相等 |

### 版本号（改这几个常量就会触发对应的全量重算）

**这张表读的是代码，不是本轮的快照。** P8–P15 收口时的值记在"P15 收口时"一列供追溯；
后面几轮各自推过版本号，`当前值` 一列必须与 `internal/` 里的常量逐字相同——
对不上就是这张表过期了，不是代码错了。

| 常量 | 当前值 | P15 收口时 | 谁推的 | 变了会发生什么 |
| --- | --- | --- | --- | --- |
| `storage.SchemaVersion` | `22` | `20` | 迁移 `021` / `022`（ADR-21） | 迁移运行器补跑到这个编号 |
| `history.ParserVersion` | `parser/8` | `parser/6` | ADR-22 逐消息 usage（`parser/7`）→ ADR-23 Codex 轮级差值（`parser/8`） | 全部转写重读，会话度量重算 |
| `eventstore.ProjectionVersion` | `projection/7` | `projection/5` | 2026-08-25 价值挖掘轮 → 2026-08-29 `<teammate-message>` 计数规则 | 命令/文件投影全部重算，会话度量随之重算 |
| `friction.ClassifierVersion` | `friction/5` | `friction/4` | 机制字典扩容（2026-08-25） | 摩擦记录重新分类，签名重算 |
| `eventstore.PairingVersion` | `pairs/1` | `pairs/1` | 未推过 | 事件配对（工具调用 ↔ 结果）重投影 |

### 未完成

1. **P7 三个月真实历史回测**：仍未做。本机历史还在累积，告警分布尚不足以做判定准确率的人工核验。
2. **摩擦→资产桥在本机产出为 0**：机制有 fixture 回放测试覆盖，但本机 49 条 `blocked by PreToolUse hook` 记录一条都没写出 hook 名字，拦截者也不在资产注册表里。这是事实，不填数。
3. **`session_commands` 的 heredoc 残留**：`cat > x <<'EOF'` 之后的正文仍被当语句读，`No` / `Event` / `PY` 这类假程序名各 ≤42 次。
4. ~~**coverage_gaps 不分项目**~~：已修（2026-08-25 规则闭环轮）——缺口按（签名 × 项目）列出，"适用的规则"=用户级资产+项目目录下资产；本机真实缺口从 1 条变 3 条（此前被其他项目规则全局遮蔽）。
5. **`/friction?from=all` 返回空集**：`from=all` 在会话端点是"不设下界"，在摩擦端点被当成字面日期。（注：第 5 条已由 §27.10 的 `rangeBound` 消除，此处按原文保留存档。）

---

## 2026-08-25 价值挖掘优化（代价与机会）

> 补记于 2026-08-25。主线：把系统从"描述发生了什么"推进到"代价与机会看得见"，
> 依据 ADR-20（insights 是既有事实的只读投影）。实测记录 `docs/qa/dogfood-2026-08-25.md`。

| 项 | 内容 | 状态 |
| --- | --- | --- |
| `/api/v1/insights` | 六类封闭洞察（中断上下文/零改动高投入/卡死循环/重复读取/覆盖缺口/缺命令），每条带判定规则原文（中英）与下钻链接；总览新增"代价与机会"区块 | 完成 |
| 机制字典扩容 | `internal/friction/hints.go` 新增 13 条规则；机制覆盖率按事件数 31% → 56%；coverage_gaps 在本机从 0 → 1 条 | 完成 |
| 时间线批量折叠 | 同刻批量记录折叠成一行事实，注意状态迁移逐条保留，kind 筛选逐条可查 | 完成 |
| 资产墙分区默认值 | "几乎未使用"默认展开，911 个"没有相关任务记录"默认折叠 | 完成 |

**验证**：check.sh 全绿；原文对账 8+8 会话 0 mismatching fields；一致性等式七处全等（1174）；CDP 六页 0 控制台错误。

**本轮明确不做**：中断轮次 token 估算（逐消息 usage 不在库里，估算即伪造）；AI 分析层（MVP 边界）；
洞察窗口对比（compare）留待下轮。

---

## 2026-08-25 规则闭环（规则层 CI，ADR-21）

> 补记于 2026-08-25。定位升级：规则层（AGENTS.md / rules / skills / hooks）的 CI——
> 简报（证据包 + 给用户 agent 的起草提示词）→ 用户写规则 → 签名验证（修复有效/未见改善/无法判断）。
> 依据：AWM/ExpeL 证明从历史归纳规则可度量提升 agent；厂商的记忆是单 harness 黑箱且无验证；
> 官方文档承认规则"只是上下文"且验证全靠手工。实测 `docs/qa/dogfood-2026-08-25.md`。

| 项 | 内容 | 状态 |
| --- | --- | --- |
| 规则简报 | `/friction` signature 分组行带 `brief`：机制、确定性落点建议（rule/hook/skill/environment/workflow）、证据与样例、可粘贴的起草提示词（中英）；Flatline 零 AI 调用 | 完成 |
| 签名验证 | 迁移 `022` `signature_watches`；POST 需显式确认；读取时评估 verified/no_change/unobservable/watching；取消保留记录 | 完成 |
| 摩擦页 UI | 签名行"简报"开关 + 验证徽标（验证中/修复有效/未见改善/无法判断）+ 复制简报 + 开始/取消验证 | 完成 |

**验证**：check.sh 全绿；新增 4 个闭环测试（简报内容/确认门禁/验证状态机含 verified→no_change 回翻/行内徽标）；
真实库上完成一次端到端（apply_patch 52 次签名 → 简报 → 建验证 → watching）。

**追加（同日晚）**：coverage_gaps 按项目分（真实缺口 1→3）；简报样例 traceback 取末行；**逐消息 usage 入事件 payload**（ADR-22，`parser/7`）+ **Codex 轮级差值归属**（ADR-23，`parser/8`）+ **payload 刷新通道**（重读可到达已存事件）——中断价签真实出数：30 天 219 次中断 156 次可测，被中断轮次合计 6.4 亿 token。
**追加（同日深夜）**：中断 token 按项目下钻（qwen 448.9M/143 次为最贵）；卡死循环 token 代价（apply_patch 52 次循环 = 505M token/15 轮）；验证记分板上总览（不动 ADR-5 通知契约）。
**追加（同日深夜收口）**：watch 判定进入通知投影（ADR-24，修订 ADR-5"唯一通知源"为"迁移+验证判定"两源，仍纯投影；watch 写入 bump data_version 使缓存失效）；简报样例优先取含 error/fail/exception 的行。
**下一拍（未做）**：重复读取的 token 代价（需"读取时体积"口径，先立决策）；watch 判定的跨会话下钻。

---

## 2026-08-29 全量跑通 + 准确性与可用性修复

> 补记于 2026-08-29。起点是在一台没有 Go 工具链、也没跑过 Flatline 的机器上从零启动，
> 再用系统自己读本机历史反过来修系统。实测记录 `docs/qa/dogfood-2026-08-29.md`。

| 项 | 内容 | 状态 |
| --- | --- | --- |
| **`main` 编译不过** | `.gitignore` 的 `coverage.*` 不带前导斜杠，在任意层级匹配，`internal/api/coverage.go` 从未进过 git。按测试 + 三个调用点 + 设计文档 §26.8 重建 | 完成 |
| **`<teammate-message>` 不是用户轮** | 另一个 agent 发来的消息被算成用户轮：2,489 次 / 528 会话，占全库"用户轮"的 **26.7%**。进 `InjectedMessagePrefixes`，`ProjectionVersion` → `projection/7` 全量重算 | 完成 |
| **重算不再关掉 API** | `RecomputeMissingSessionStats` 跑在 `net.Listen` 之前，版本号一推就是 197 s 全黑且看不到进度（违背 ADR-18 §4）。移进 `catchUp()`。A/B：302 s → **0 s**，结果同为 `backfilled sessions=973` | 完成 |
| **对账脚本的真相收窄** | `audit_accuracy.py` 的"任何尖括号开头都算注入块"启发式会把真实用户正文判成注入、把新 harness 标签悄悄吞掉。改用与 Go 相同的闭合表；未登记标签改为 `[warn]` 单独报出；新增 `TestAuditScriptMirrorsInjectedPrefixes` 执行"两边保持一致"这句注释 | 完成 |
| **morphdom 被 `.gitignore` 吃掉** | `vendor/` 同样不带斜杠，吃掉 `internal/web/static/vendor/`；`/vendor/morphdom.js` 由 SPA 兜底返回 `index.html`，浏览器报 `Unexpected token '<'`，局部更新退化成整块重建。而 `web_test.go` 只断言 200 + 非空，**在兜底响应上通过**。重新 vendor + 测试改为拒绝兜底 | 完成 |
| **新门禁 hidden sources** | `scripts/check.sh` 新增一步，读 `git ls-files --others --ignored`：被 `.gitignore` 藏起来的源文件一律 `exit 1`。gofmt/vet/test 读工作区，读不到这一类问题 | 完成 |
| **机制字典扩容** | 新增 7 条规则；按事件数覆盖率 **49.3% → 59.3%**（新解释 386 条），并纠正 7 条把"分类器够不到模型"错记成"测试自己报了失败"的归类；`hints.go` 的顺序注释改成代码真正在做的事 | 完成 |

**验证**：`scripts/check.sh` 全绿；原文对账 claude_code 8 + codex 8 会话 **0 mismatching fields**（修复前 2）；
一致性等式七处全等 973；浏览器首页 **0 控制台报错**（修复前 1 条）。

**同日追加（ADR-25 / P16 前三拍全部落地）**：
- **舰队汇总**：`GET /api/v1/sessions/{id}/fleet` + 会话详情"团队"区块——真实 15 子代理会话实测
  总 592.7M token / **工作 token 10.1M**（58 倍失真当场可见）；结局证据只说"未见失败"。
- **标题剥离**：65 个 `<teammate-message …>` 原文标题 → 0，如实标 `synthesized`，库中原文不动。
- **"现在"视图（P16-3）**：`GET /api/v1/now`（no-store、无 ETag——已结束的运行不能被 304 钉在屏幕上）
  + 总览首屏"正在进行"区；安静时不渲染。
- **工作 token 领衔（P16-2）**：`work_tokens = input + cache_write + output` 进入 usage 聚合、
  期摘要与 delta；总览 KPI 换位（30 天窗口实测：工作 277.6M vs 总 31.2B——112 倍）。
- 价值复盘见 `docs/flatline-value-review-2026-08-29.md`；规则闭环第一次完整走通
  （coverage gaps 5→0，两条 watch 基线冻结，2026-09-12 前后出判定）。

**本轮明确不做**：`pkill` 自匹配单列机制（需先确认能否只凭已记录事实判定）；
美元换算（外部易变事实，工作 token 已承担代价读数）。

---

## 跨阶段约束（所有阶段适用）

1. **MVP 边界**：账户/云同步、AI 分析、桌面壳、群体参考、插件运行时、正式统计改善轮次——任何阶段均不实现（系统设计 §8、附录 B）。
2. **证据纪律**：禁止伪造数据、禁止因果结论、禁止无证据健康分（AGENTS.md §2）。
3. **写入纪律**：所有写入或删除资产必须显式确认，清理可回滚（AGENTS.md §3）。
4. **隐私边界**：仅 loopback、无上传、无遥测、无登录（DEVELOPMENT.md §8）。
5. **可重放性**：派生层携带 detector/schema 版本，阈值调整后全量回放状态历史必须廉价（ADR-10）。
6. **阶段完成定义**：验收标准逐项通过 + 测试全绿 + 文档同步 + 交付四件套（变更摘要/测试/风险/未完成项）。
