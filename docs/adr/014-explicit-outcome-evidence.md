# ADR-14: 修改后验证使用显式结果证据

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline maintainers

## 背景

`awaiting_resurrection` 的验证规则要求“重新参与且未违背”。单独记录
`asset_invoked` 只能证明资产被调用，不能证明后续行为遵循了资产要求；
如果把缺少违背记录当成“没有违背”，就会违反“缺失 ≠ 零”和 unknown 不得
伪装成事实的约束。

## 决策

Source Adapter 只有在源数据明确给出结果时，才写入以下 canonical 事实：

- `asset_observed_use` + `participation_signal=followed`：明确记录行为遵循；
- `asset_violation` + `payload.violated=true`：明确记录调用后违背。

修改后验证只有在同一资产、同一 Session 内同时存在 Exact 调用和明确的
`followed` 参与记录时才通过；只有调用记录时保持验证中；出现 Exact 调用
与 Exact 违背链时进入 `bypassed`。`followed=false` 或缺少结果字段都不
自动生成事实。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 调用后默认视为遵循 | 实现简单、反馈快 | 把未记录伪装成通过，违反证据纪律 | 拒绝 |
| 仅增加一个“未违背”布尔字段 | 查询简单 | 无法保留调用、遵循、违背的 locator 与观测等级 | 拒绝 |
| 写入独立结果事件（本决策） | 事实可重放、可下钻、可区分 unknown | 适配器需要显式映射源字段 | 采纳 |

## 后果

正面：修改后验证不再因数据缺失而误报成功；Bypass 判定可以复用同一条
append-only 事件链，并保留来源 locator。

负面：当前数据源没有提供结果字段时，资产会保持“修改后验证中”，这是
有意的保守结果；后续适配器必须只映射真实可观察字段，不能补写默认通过。
