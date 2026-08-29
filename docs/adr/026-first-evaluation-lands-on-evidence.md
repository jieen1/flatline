# ADR-26: 首次评估落在证据上，不经过强制的 dormant 中停

- 状态：accepted
- 日期：2026-08-29
- 决策者：用户（"首启假 dormant 修法待拍板"，授权持续推进）+ 主 agent 提案
- 起因：`docs/qa/backtest-2026-08-29.md` §4 —— P7 真实历史回测的发现

## 背景

首次导入的真实时间线：12 个 skill 在首轮评估被判 `(new) → dormant`，下一轮评估
（约一个导入周期后）11 个转 healthy、1 个转 no_opportunity。窗口期内墙上显示
"几乎未使用 12"且该分区默认展开；当天上午的 dogfood 记录把 `dormant: 12` 当实测状态
写了下来——**作者本人被误导，即为问题真实性的证据**。

回测记录当时猜测是时序问题，深查后根因更干净：`choosePrimary` 对
`PreviousState == ""` **无条件**返回 dormant（注释称"中性起点"），且这条分支排在
参与证据检查之前——首轮评估时参与记录早已在库里，判定却不看它。闪烁是构造性的。

## 决策

1. **首次评估落在证据上**：`PreviousState == ""` 且 `ParticipationObserved` → 直接 healthy；
   没有参与证据的才取中性 dormant 起点（有机会、无参与记录，dormant 是诚实读法）。
   不新增状态枚举——回测记录里设想的 `pending_history` 独立态不需要了，
   因为问题不是"历史没读入"，是"读入了却不看"。
2. **首个状态落在 healthy 不告警**：healthy 告警的语义是"恢复"（修改后验证通过），
   从无到有谈不上恢复；不加这条，修复 1 会让首次导入每个健康资产各发一条恢复通知。
   **首个状态落在 silent / broken / bypassed / degraded 照旧告警**——那是回测点亮墙
   之后监护仪开口说话，正是它的本职（验收标准 1/3）。

## 后果

- 正向：首启不再有"假 dormant"窗口；有证据的资产第一屏就是 healthy；
  首次导入不产生恢复告警噪音；回测发现的告警照发。
- 已有安装不受影响：改动只作用于 `PreviousState == ""`（资产的第一次评估），
  已记录状态的资产走原有路径；判定历史 append-only，不回写。
- 测试演进（非绕过）：三处钉旧契约的测试逐一改写并注明依据
  （machine_test 首评有证据 → healthy；repository_test 首评序列与行数；
  api/assets_test 夹具首态 healthy）。
- 验证：合成测试全绿 + 一次真实历史的全新首次导入回放，预期迁移里
  不再出现"dormant 中停→healthy"对，见 dogfood 记录追加节。
