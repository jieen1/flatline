# ADR-3: 自建 canonical schema，AgentView 类工具为解析参考/兼容输入

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline 设计（系统设计 v0.4，沿用 v0.3）

## 背景

Flatline 的输入是 CC 与 Codex 的本地 Session 历史，各数据源格式随版本演进且字段覆盖不一。需要一个稳定的内部事件模型（canonical schema），使观测等级、locator、append-only 语义（系统设计 v0.4 §3.1、§6）不随数据源格式漂移。

## 决策

Flatline 自建 canonical schema（Canonical Event Store：append-only + locator + EnvironmentChanged 锚点）；AgentView 类工具仅作为解析参考与兼容输入，不作为数据模型权威。各 Source Adapter 通过字段矩阵、fixture 规范与版本探测把源数据映射到 canonical 事件，且不得新增 `observation_level` 取值。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 自建 canonical schema（本决策） | 观测等级/证据纪律由 Flatline 控制；数据源格式变化只影响适配器 | 需自维护解析与 fixture | 采纳 |
| 直接复用 AgentView 数据模型 | 省解析工作 | 模型不携带 Flatline 的观测等级与 locator 语义，证据纪律无法保证 | 拒绝 |
| 每数据源独立模型、查询时对齐 | 贴近源格式 | 派生层需处理 N 套模型，状态机无法统一 | 拒绝 |

## 后果

- 正面：证据纪律（AGENTS.md §2）落在 schema 层，`observation_level` 封闭枚举随数据携带；数据源升级只改适配器。
- 负面：适配器是长期维护成本（字段矩阵 + fixture 回归）；新数据源接入需先扩展字段矩阵。
- 对 MVP 边界：MVP 只承诺 CC + Codex 两个适配器（roadmap P2）。