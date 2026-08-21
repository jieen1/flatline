# ADR-15: Native session transcript metadata and bounded evidence events

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline maintainers

## 背景

Flatline 的 native history reader 当前只把命中资产的工具调用转换成 canonical event。原始 Claude Code 与 Codex JSONL 实际还包含用户消息、助手消息、工具调用和工具结果，因此现有会话会错误地表现为只有 `session_started` 加一两个资产事件，详情页也只能显示 source session ID。

会话详情需要同时满足两个边界：

1. 会话标题和任务文本必须来自本地 transcript 中的明确字段或可定位的首条用户任务文本，不能凭空生成；
2. 详情需要有可下钻的真实轨迹，但不能把完整原始 transcript 或思维内容复制到 canonical store。

## 决策

扩展 source-independent session metadata，并把有明确 locator 的、经过长度限制的 transcript 记录作为 append-only canonical evidence events 保存到 daemon-owned SQLite：

- `sessions.title` 保存来源明确的会话标题：Claude 优先使用 native `ai-title`，否则使用首条可识别用户任务文本；Codex 使用首条可识别用户任务文本；没有证据时保持 NULL；
- `sessions.task_text` 保存首条可识别用户任务文本的有限长度摘要；它是展示与下钻入口，同时由 native reader 的封闭关键词规则产生可复现的 task tags；标签来源不会使用模型、会话 ID、事件数量或资产数量；
- native reader 只在 bounded task text 可识别时创建 task shape；机会集合只来自 task text 的精确资产路径/不歧义 basename 或 transcript tool input 中的明确资产引用，绝不从会话存在或 cwd 相同推导 opportunity；
- `transcript_message` 保存 user/assistant message 的有限长度文本摘要；
- `transcript_tool_call` 保存工具名和有限长度输入摘要；如果同一条工具调用命中了资产，则同一 event 继续携带资产参与证据；
- `transcript_tool_result` 保存工具结果的有限长度摘要；
- 所有 transcript evidence event 的 `observation_level` 为 `unknown`，除非同一 event 同时携带已有的明确资产调用观测等级；transcript 的存在不等于资产参与；
- 每条记录保留 source path + line/message locator，并使用稳定 source event id 幂等重放；重复扫描不重复插入；
- 不保存 thinking/reasoning、完整 raw bytes、未限定长度的工具输出或 source 文件修改内容；超长文本以 `truncated` 元数据标识；
- sessions 列表/详情 API 同时返回 title、task_text、transcript_count，并保留原始 source_session_id 作为机器定位字段。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 继续只展示 source session ID | 无 schema 变化 | 丢失真实任务语义，无法解释事件数量 | 拒绝 |
| API 请求时重新读取 native transcript | 不增加 SQLite 字段 | 页面依赖源文件仍在、每次读全量文件，无法保证历史一致性 | 拒绝 |
| 保存完整 raw transcript | 详情最完整 | 放大隐私与存储风险，违反 raw source bytes 不进入 canonical store 的边界 | 拒绝 |
| 保存 bounded metadata + transcript evidence events | 标题、轨迹、定位可重放；保留缺失与未知边界 | 需要迁移和 source adapter 版本升级；正文可能被截断 | 采用 |

## 后果

会话事件数会从“会话开始 + 资产命中”变成真实记录的消息/工具轨迹，因而显著增加；这是真实采集结果，不是演示数据。旧数据库通过 additive migration 保留既有事实，新字段在重新扫描 native 文件后逐步补齐；没有重新扫描过的历史会话仍显示未记录，而不是用 ID 猜标题。已有历史在下一次 native 重扫时补齐机会与任务形状；没有明确资产证据的会话继续保持没有机会记录。

UI 必须把标题、任务文本、消息/工具/资产事件分开表达，不能把 transcript message 误算为资产参与率的分子或任务机会分母。所有缺失字段继续显示“未记录”，并说明缺失来源。
