# 架构决策记录（ADR）· Flatline

本目录存放 Flatline 的架构决策记录（Architecture Decision Records）。
ADR 是**决策的权威来源**：当设计文档、代码与 ADR 冲突时，以最新 ADR 为准，并同步修订其他两处。

## 何时需要 ADR

满足以下任一条件，必须先落 ADR 再动手：

- 新增/移除顶层模块（`internal/` 下新包、新 daemon 组件）；
- 改变数据模型或 SQLite schema 的结构性设计（非普通迁移）；
- 改变运行形态（进程模型、API 形态、存储选型）；
- 推翻或修订既有 ADR；
- 引入新依赖（尤其网络/存储/前端框架类）；
- 涉及隐私边界、写入纪律、证据纪律的例外或放宽。

普通 bug 修复、阈值调整（在既有 ADR 约束内）、UI 细节不需要 ADR。

## 编号与命名

- 文件名：`NNN-<短标题>.md`，三位编号递增（`001-local-first.md`）；
- 编号永不复用；被推翻的 ADR 保留原文，状态改为 `superseded` 并链接新 ADR。

## 模板

```markdown
# ADR-N: <标题>

- 状态：proposed | accepted | superseded by ADR-M | rejected
- 日期：YYYY-MM-DD
- 决策者：<谁>

## 背景

<问题与约束，引用设计文档章节>

## 决策

<一句话决策 + 关键细节>

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |

## 后果

<正面/负面后果、维护成本、对 MVP 边界与隐私边界的影响>
```

## 既有决策（源自系统设计 v0.4 §10）

以下 ADR 已在系统设计 v0.4 中确立，编号沿用；正式文件已于 P1 期间文件化（roadmap P1 交付物）：

| 编号 | 标题 | 状态 | 文件 |
| --- | --- | --- | --- |
| ADR-1 | 本地优先 | accepted（沿用 v0.3） | [001-local-first.md](001-local-first.md) |
| ADR-2 | 单 daemon + SQLite + Go 单二进制 | accepted（沿用 v0.3） | [002-single-daemon-sqlite-go.md](002-single-daemon-sqlite-go.md) |
| ADR-3 | 自建 canonical schema，AgentView 类工具为解析参考/兼容输入 | accepted（沿用 v0.3） | [003-canonical-schema.md](003-canonical-schema.md) |
| ADR-4 | 预期来自资产自身历史（基线可解释、可下钻，不做跨用户比较） | accepted | [004-self-history-baseline.md](004-self-history-baseline.md) |
| ADR-5 | 告警是状态迁移的投影（不可独立创建/删除，同状态不重复告警） | accepted | [005-alerts-as-transition-projection.md](005-alerts-as-transition-projection.md) |
| ADR-6 | 验收降级为复活检测（单事件验证；统计验收进入可选进阶模式） | accepted | [006-resurrection-as-acceptance.md](006-resurrection-as-acceptance.md) |
| ADR-7 | 删除是一等结局（清理与修复同等重要，收益确定性计算，可回滚） | accepted | [007-deletion-as-first-class-outcome.md](007-deletion-as-first-class-outcome.md) |
| ADR-8 | 判定规则必须一句话可解释（一行分子分母说不清则不得上线） | accepted | [008-one-line-explainable-rules.md](008-one-line-explainable-rules.md) |
| ADR-9 | 确定性优先、AI 署名、显式授权写入 | accepted（沿用 v0.3 ADR-7/8） | [009-determinism-ai-attribution-explicit-writes.md](009-determinism-ai-attribution-explicit-writes.md) |
| ADR-10 | 状态历史可廉价重放（阈值/detector 升级后全量回算，回测即验收） | accepted | [010-cheap-replay-of-state-history.md](010-cheap-replay-of-state-history.md) |

## 流程

1. 起草 ADR（proposed），在 PR 中与设计/代码变更一并提交；
2. 审查：核对与证据纪律、写入纪律、MVP 边界、隐私边界（AGENTS.md §2/§3/§6/§7）的兼容性；
3. 合入后状态改为 accepted；
4. 后续推翻：新 ADR 标注 `superseded by`，旧文件不改写历史。