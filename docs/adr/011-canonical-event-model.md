# ADR-11: Canonical event model and append-only ingestion contract

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline 设计与实现

## 背景

P2 需要把 Claude Code 与 Codex 的本地会话映射到稳定的 canonical 事件模型。源格式会变化，字段覆盖也不一致；同时，事件必须可重放、可幂等、可下钻，且缺失字段不能被伪装成零值。

## 决策

Flatline 使用 `internal/canonical` 作为唯一事件模型：

- `observation_level` 与 `participation_signal` 是两个互相独立的封闭枚举；
- 可缺失的位置和时间使用 nil，不能用零值代替；
- 每个 adapter 产出的事件必须提供确定性的 `source_event_id`，重复摄取不得产生重复事件；
- 每个事件必须带有包含具体源位置的 locator；
- `internal/eventstore` 只暴露 INSERT 与只读查询，不提供事件 UPDATE/DELETE；
- EnvironmentChanged 是同一 source 内按 `started_at`、再按 session id 排序比较 harness/model 得出的 `inferred` 对齐锚点，不表达因果。

适配器只负责源格式到 canonical 模型的映射，不得新增 observation level。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 自建 canonical 模型 + 共享事件存储 | 证据纪律和回放语义统一 | 需要维护适配器与 fixture | 采纳 |
| 每个源独立模型，查询时再转换 | 初期贴近源格式 | 派生层需要处理多套语义，缺失规则容易漂移 | 拒绝 |
| 允许无 source_event_id 的事件 | 可保留更多不完整输入 | SQLite partial unique index 无法保证重复摄取幂等 | 拒绝 |

## 后果

- 正面：适配器升级不会改变状态机输入语义；重复摄取、locator 和环境对齐可测试且可重放。
- 负面：适配器必须为每条事件生成稳定 id；真实源格式变化需要更新字段矩阵和 synthetic fixture。
- MVP 边界：本 ADR 不实现资产快照、机会追踪、detector、UI 或真实资产写入。
