# ADR-24: 修复验证判定进入通知投影

- 状态：accepted
- 日期：2026-08-25
- 决策者：主 agent 提案（修订 ADR-5 的通知源规则）

## 背景

ADR-5 规定"状态迁移是唯一通知源"，通知是迁移的纯投影、无第二个可变存储。该规则制定时
修复验证（ADR-21 的 signature watch）尚不存在。验证闭环的判定——"修复有效 / 未见改善 /
无法判断"——恰恰是监护仪应当响起的时刻：用户写完规则后不应需要盯着页面等结果。

## 决策

`/api/v1/notifications` 增加第二个投影源：**watch 判定**。规则修订为
"状态迁移与修复验证判定是通知的两个投影源，均为只读投影、无第二个可变存储"：

1. 判定条目来自 `signature_watches` 的当前评估结果（复用 `loadWatches` 的读取时评估），
   仅收录 `verified` / `no_change` / `unobservable` 三种结论；`watching` 未出结果、
   `cancelled` 已被用户撤回，都不进通知。
2. 去重键 = (watch, status)：同一 watch 的同一判定只出现一条；watch 被取消后条目消失
   （取消即撤回，与资产通知的忽略处置对位）。
3. 条目形状：`kind` = `watch_verified` / `watch_no_change` / `watch_unobservable`，
   `id` = 负的 watch id（与迁移 id 空间不相交，客户端隐藏键因此稳定），
   `signature` / `watch_id` 携带下钻，资产字段为空。排序与迁移条目按时间合流。
4. 判定的发生时间取 `resolved_at`（verified）或 `last_evaluated_at`（其余），
   都是判定实际写回的时刻，不是读取时刻。

## 后果

- 正向：闭环的最后一步——写完规则不用回来盯，监护仪在出结果时响起；
  通知语义保持"投影而非存储"，回放确定性不变。
- 代价：`/notifications` 的消费方需要容忍资产字段为空的条目（前端同轮更新）；
  契约 §27.5 同步修订。
