# ADR-22: 逐消息 usage 进入事件 payload（把中断折成钱的第一步）

- 状态：accepted
- 日期：2026-08-25
- 决策者：用户（产品方向指令）+ 主 agent 提案

## 背景

事件层的 `transcript_message` payload 只存 `{message_id, role, text}`，token 只有会话级聚合
（`session_usage`）。这使"被中断的轮次烧了多少 token"不可计算——而它是洞察层（ADR-20/21）
最想回答的数字：中断是用户用脚投的票，轮次 token 是那张票的价签。

约束：payload 是 append-only 事实（ADR-11/17），不能改已存事件；解析器有版本化的全量重读
通道（`history.ParserVersion`，ADR-10 同源思路）；来源能力不同必须如实标注分母。

## 决策

1. **解析器**在规范化转写的 assistant 消息上附带该消息自己的 usage
   （`{input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, reasoning_tokens, total_tokens}`），
   数值逐字来自源记录，不重算、不补零。`history.ParserVersion` 推到 `parser/7`：
   全部转写在下次启动时重读，事件按 locator 幂等重写。
2. **本轮只覆盖 Claude Code**：它在每条 assistant 记录上写了消息级 usage，是记录级事实。
   Codex 的 `token_count` 是会话运行总量——按轮差值归属属于推断，留待后续（届时单独立决策）；
   opencode/dsh 消息级无记录。未覆盖的消息没有 usage 键，不是 usage 为零。
3. **消费方**：`/api/v1/insights` 的中断块新增 `turn_tokens_total` / `turn_measured`——
   对每条中断，取它与前最后一条用户消息之间 assistant 消息的 usage.total_tokens 之和；
   只统计消息级记录了 token 的来源，分母（可测中断数）随事实携带。判定规则原文同步更新。

## 后果

- 正向：中断第一次有了价签（本机 30 天可测中断的轮次 token 合计直接可见）；
  该字段是后续一切"turn 级成本"分析（卡死循环代价、上下文增长曲线）的地基。
- 代价：全量重读一次（本机约 1172 个文件，启动期一次性）；payload 略增大（每条 assistant
  消息 +~120 字节）。
- 不做：Codex 差值归属（推断）；用会话级总量按比例分摊到轮次（伪造精度）。
