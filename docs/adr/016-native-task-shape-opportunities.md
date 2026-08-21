# ADR-16: Native task shape and exact asset opportunities

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline maintainers

## 背景

Native Claude Code 与 Codex JSONL 没有稳定的 task-shape 枚举，因此旧 reader 只能把 `TaskTags` 留空。结果是所有 native 会话都落入“没有相关任务记录”，即使 transcript 已经保存了真实的用户任务和明确的工具路径。

## 决策

从有界、来源明确的首条用户任务文本按封闭关键词规则生成确定性 ASCII tags；同一会话的 opportunity 集合只来自精确资产路径、项目相对路径、唯一 basename，或已经由 native 工具输入解析出的明确资产引用。缺失任务文本、歧义 basename、仅有 cwd 或仅有 session/transcript 记录均不生成机会。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 把每个 native session 都视为每个资产的机会 | 能快速得到非零分母 | 把会话存在误当任务相关性，违反证据纪律 | 拒绝 |
| 用模型从 transcript 推断任务分类 | 语义覆盖更广 | 不确定、不可重放、超出 MVP 且引入隐私/依赖风险 | 拒绝 |
| 只依赖 source adapter 的显式 task-shape 字段 | 边界最窄 | 当前 native wire format 没有该字段，页面长期没有任务机会 | 拒绝 |
| 封闭规则解析 bounded task text + 精确资产引用 | 可重放、可测试、无需网络；歧义仍保持缺失 | 新任务词需要维护规则 | 采用 |

## 后果

Opportunity 仍然是证据投影，不是 session 数量的别名。规则输出随 `shape/1` 存储并可从 canonical transcript 重放；未来规则改变必须递增 shape rule version。没有明确资产引用的真实会话仍会显示“没有相关任务记录”，这是数据边界而不是失败或零值。
