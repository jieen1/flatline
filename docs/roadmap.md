# Flatline · 路线图（Roadmap）

> 版本：v1.0 · 2026-08-19
> 依据：`flatline-system-design-v0_4.md`（系统设计）与 `flatline-ui-design-guidelines-v2_0.md`（UI 指南）。
> 产品定位：**本地优先的 Agent 资产生命体征监护仪**。
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
| P6 | 诊断/时间线/处置/复活/清理 | 四拍核心流程全链路走通 | 部分（处置与复活有依赖） |
| P7 | 回测与发布 | 真实历史回测验收 + 单二进制发布 | 否（发布串行） |

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

**交付物**：
- Asset Snapshotter：资产发现（Skill/AGENTS.md/Rule/Hook）、内容哈希、版本切分、观测升级器（快照跃迁规则）；
- Asset Registry：资产/版本注册与查询；
- Effective Bundle Resolver：版本向量强制关联（Session ↔ 当时生效的资产版本集）；
- Opportunity & Participation Tracker：任务形状归类、机会计数、参与记录（分观测等级）、基线滚动窗口计算（参与率 + 绝对次数）；
- 基线可解释：分子/分母/窗口/阈值版本可查询。

**验收标准**：
- [ ] 资产修改后产生新版本，EffectiveBundle 可回答"某 Session 当时生效的是哪个版本"；
- [ ] 基线计算在 fixture 上可人工核验（参与率与绝对次数均正确）；
- [ ] 参与记录全部携带观测等级；loaded/offered 仅在数据源支持时记录并如实标注；
- [ ] 形状归类规则文档化（什么算"同类任务"），归类结果可下钻到判定依据；
- [ ] 派生层携带 detector/schema 版本，支持整体重算。

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
- 其余诊断/处置组件（Cause Candidate、Funnel Compare、Reference Check Report、Prune Sheet、Resurrection Toast、Diff Viewer、Evidence Block、Session Reference、Timeline Track）在 P6 完成；
- 墙的分区结构：需要注意 / 观察中 / 健康 / 休眠（折叠+清理入口）/ 暂无机会·不可观测·已归档（折叠灰度）；
- `go:embed` 内嵌构建产物，单二进制交付。

**验收标准**：
- [ ] 单二进制启动后浏览器打开 loopback 地址即可使用，无外部资源请求；
- [ ] 只看首页能回答"哪些资产死了、哪些在耗上下文"；
- [ ] 每个沉默判定可见分子、分母、基线和机会定义原文；
- [ ] 每条 sparkline 有变化标记点（资产版本=实心、环境变化=空心）；
- [ ] 无"待办数"、无健康分排行、无因果句式、无空白式 unknown；
- [ ] 前端 lint/test/build 全绿；"暂无机会"与"不可观测"有独立灰度表达。

**依赖**：P4（状态与迁移数据）。
**允许并行**：是——API 端点与前端组件可并行（API 契约先行冻结）；时间线页与墙可并行。

---

## P6 · 诊断/时间线/处置/复活/清理

**目标**：四拍核心流程（监护 → 告警 → 诊断 → 处置 → 复活）全链路走通，批量清理流可用。

**交付物**：
- 诊断页完整实现：判定依据卡、候选原因（证据前置，含"未知原因"诚实示例）、参与漏斗现在 vs 基线、处置历史；
- 四种处置：修改（Diff/外部编辑器，保存后进入 awaiting_resurrection）、清理/删除（确定性收益展示 + 显式确认 + 可回滚记录，不物理删除）、归档（停止监护可恢复）、忽略（持久化为 Disposition，当前状态实例内静默，见系统设计 §4.3）；
- 复活确认：awaiting_resurrection 期间首次重新参与且未违背 → healthy + 复活通知（直达 Session 调用位置）；N 个机会仍零参与 → 复活失败通知；
- 批量清理流：休眠资产表格、合计上下文占用、批量归档/保留、移除清单生成、一键回滚；
- 变化时间线页完整实现：三类事件同轴、按资产过滤、聚簇提示（陈述句）；
- 通知体系：问题/复活通知仅由状态迁移产生、同状态零重复、通知自带判定依据原文；休眠月度汇总是非告警摘要，不改变状态迁移规则。

**验收标准**：
- [ ] 从沉默告警到完成一次修改并收到复活通知，全流程无需离开产品（外部编辑器除外）；
- [ ] 批量清理给出确定性收益数字并生成可执行清单，可回滚；
- [ ] 所有资产写入/删除操作有显式确认（AGENTS.md §3），无静默修改；
- [ ] 候选原因全部证据前置，存在"未知原因（无支持证据）"示例；
- [ ] 复活通知一键直达那个 Session 的调用位置；
- [ ] 时间线页可演示"多个资产沉默聚簇在同一环境变化附近"（陈述句，不判因果）。

**依赖**：P5（API 与页面骨架）。
**允许并行**：部分——诊断页/时间线页/清理流可并行；处置与复活确认共享状态机写入路径，需串行收口。

---

## P7 · 回测与发布

**目标**：在开发者 ≥3 个月真实历史上回放状态机完成回测验收，产出可发布的单二进制。

**交付物**：
- 回测工具（`scripts/backtest`）：真实历史回放 + 判定结果导出（人工核验用）；
- 回测报告：每个被判沉默/休眠/失效的资产人工核验记录；
- 发布物：单二进制（跨平台，纯 Go 无 CGO）、安装说明、首次使用流程（历史回测即刻点亮墙）；
- 文档定稿：README、设计文档、UI 指南、ADR 与实现一致性核对。

**验收标准**（对应系统设计 §9）：
- [ ] 回测正确性：人工核验判定准确率达标（判定准确率是产品成立的前提）；
- [ ] 任意告警可下钻到判定依据原文、机会列表、原始 Session 事件与当时资产版本；
- [ ] 首次导入完成后，生命体征墙即刻呈现有意义的状态分布，无需等待新数据；
- [ ] 从沉默告警到复活通知全流程走通（P6 验收在真实数据上复验）；
- [ ] 批量清理给出确定性收益数字并生成可执行清单；
- [ ] 每个比例可见分子分母；unknown 与 inferred 永不伪装成事实；
- [ ] 全流程不依赖登录与上传；
- [ ] UI 指南 §12 原型验收清单逐项通过（6 步黄金路径 + 清理路径连续点击走通）。

**依赖**：P6（全链路功能）。
**允许并行**：否——回测、发布物、文档定稿串行收口（回测发现判定问题需回退 P4 修复后重放）。

---

## 跨阶段约束（所有阶段适用）

1. **MVP 边界**：账户/云同步、AI 分析、桌面壳、群体参考、插件运行时、正式统计改善轮次——任何阶段均不实现（系统设计 §8、附录 B）。
2. **证据纪律**：禁止伪造数据、禁止因果结论、禁止无证据健康分（AGENTS.md §2）。
3. **写入纪律**：所有写入或删除资产必须显式确认，清理可回滚（AGENTS.md §3）。
4. **隐私边界**：仅 loopback、无上传、无遥测、无登录（DEVELOPMENT.md §8）。
5. **可重放性**：派生层携带 detector/schema 版本，阈值调整后全量回放状态历史必须廉价（ADR-10）。
6. **阶段完成定义**：验收标准逐项通过 + 测试全绿 + 文档同步 + 交付四件套（变更摘要/测试/风险/未完成项）。