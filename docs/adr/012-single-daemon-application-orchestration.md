# ADR-12: 单 daemon 应用编排与可重放派生链路

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline maintainers

## 背景

P1/P2 已建立 loopback daemon、SQLite 迁移、canonical event store 和 source adapters；P3 已建立资产版本、EffectiveBundle、机会/参与与基线核心库。但这些模块尚未连成持续监护产品：daemon 不摄取资产、没有 detector/Vital State Machine、API 只有健康检查，也没有内嵌 SPA。

系统设计 v0.4 §4.1、§6、§7 与 roadmap P4-P7 要求：事实层可追溯、派生层可廉价重算、状态迁移是告警唯一来源、daemon 是唯一数据属主。AGENTS.md 另外要求源资产写入必须显式确认、缺失不得伪装为零、告警规则必须一行可解释。

## 决策

在已有事实层与 P3 模块之上增加一个单 daemon application orchestration boundary：

1. `internal/ingest` 负责只读 source/asset 输入、幂等摄取、扫描状态和错误报告；它不直接生成状态。
2. `internal/detectors` 只实现确定性、无副作用的 detector；每个 verdict 包含 evidence、observation level、numerator/denominator、rule/schema version 和 locator。
3. `internal/vital` 是唯一状态裁决者，集中阈值与迁移表；它持久化 `vital_states`/`state_transitions`，保证每个资产只有一个 open primary state，并允许从 append-only facts 重放派生历史。
4. `internal/api` 只通过 daemon-owned DB 的 read model 暴露版本化 loopback API；CLI 通过 API 查询而不直写数据库。
5. `internal/web` 提供零依赖、相对 URL 的静态资源并通过 `go:embed` 编译进单二进制；运行时不依赖网络或外部资源。
6. 所有未来 disposition/mutation service 默认拒绝未确认的资产源写入/删除；清理只生成可回滚记录与移除建议，不物理删除。

事实表和派生表保持边界：重算可以清除并重建派生状态，但不能改写 canonical facts、原始 locator 或资产源文件。所有版本号随结果持久化，保证阈值升级后的历史差异可解释。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 重写为新的单体应用 | 入口表面简单 | 丢失已验证事实层，回归和迁移风险最大 | 拒绝 |
| 各 API/CLI/worker 独立写 SQLite | 局部开发快 | 破坏单 daemon 数据主权，容易产生竞态和未记录写入 | 拒绝 |
| 在现有模块上增加编排与纯派生层 | 保留证据链；边界可测试；支持 replay/backtest | 需要维护明确接口与版本 | 采用 |

## 后果

正面：首次导入、持续摄取、detector、状态、API、UI 都有单一事实来源；synthetic/真实历史可用相同路径回放；隐私和写入边界可以在应用入口集中测试。

负面：需要新增若干内部包和迁移/DTO；P4-P7 必须按依赖顺序完成，不能把单元结构误认为产品完成；前端只能使用无依赖静态实现，浏览器验收仍需要运行环境。

与 MVP 边界一致：不引入账户、云、AI、桌面壳、插件运行时、群体参考或 ImprovementCycle。
