# Flatline 后续核心方向 · 2026-08-29 调研结论

> 输入：本机 4 个月真实历史（977 会话 / 35.6 万事件）上一周的深度使用与修复记录
> （`docs/flatline-value-review-2026-08-29.md`、`docs/qa/dogfood-2026-08-29.md`、`docs/qa/backtest-2026-08-29.md`），
> 加 2026-08-29 的外部调研（本地工具生态、厂商动向、研究综述、公开痛点；来源见文末）。
> 结论先行：**核心方向 = 经验层的 CI——用你自己的真实历史，纵向证明每条规则/技能值不值得存在；
> 舰队是它的作业单元，事实层存档是它的护城河。**

## 1. 外部地形：三层市场，两层已经没有位置

| 层 | 谁在做 | 对 Flatline 的含义 |
| --- | --- | --- |
| **用量/成本报表**（读本地 JSONL 出账单） | ccusage（18k★）、ccflare、Sniffly、Claude-Code-Usage-Monitor、CCHV（七 harness 历史查看器） | **红海**。解析 JSONL 出日/月/会话成本已是 npm 一条命令；继续投入是和一群免费工具比谁的表格好看 |
| **组织级可观测**（SDK/网关接入） | Anthropic 官方 Analytics API + 企业仪表盘 + OTel；LangSmith Fleet、AgentOps、Arize 等 | **厂商与平台的地盘**。它们要求接入、面向团队采购；Anthropic 自己已给企业发人/天粒度的 lines/commits/PR 数据 |
| **经验层生命周期**（规则/技能/记忆的证据化管理与验证） | **没有人**。2026 年 124 篇论文的技能库综述原话："no system addresses the full lifecycle: conflict detection, staleness recognition, principled deprecation, cross-level consistency" | **这是 Flatline 已经站进去的空位**：静默/休眠检测=staleness，带回滚的清理=principled deprecation，签名验证=verification |

两个佐证这层正在升温：

1. **规则遵守率成了公开话题，但全是静态/实验性测量**：2026 年的公开基准给出"无 CLAUDE.md 时 0/524 遵守"
   "长会话内每多生成一个函数，遵守几率降约 5.6%"这类结论；已有 cc-audit 之类工具给 CLAUDE.md 打静态分。
   **没有人从用户自己的真实历史里纵向测**——而那才回答"我这条规则在我的项目里有没有用"。
2. **转写留存成了公开痛点**：Claude Code 默认 30 天静默 `unlink()` 全部转写（GitHub 高热 issue），
   `cleanupPeriodDays: 0` 还有"反而完全不写转写"的 bug。**harness 在删证据，Flatline 在存证据**——
   跑得越久，SQLite 里的事实层越是唯一副本。

## 2. 内部证据：四个月数据把用户画像和价值分布讲清了

| 事实 | 读数 | 方向含义 |
| --- | --- | --- |
| 舰队是常态 | 69% 会话是子代理；每周 4–10 支舰队；Agent Teams 2026-02 官方化（本机 2,489 条 teammate-message 就是它的痕迹） | 改进闭环的**作业单元是舰队**，不是单会话 |
| 代价的真话 | 98% token 是缓存读取；工作 token 才是成本形状 | 成本报表红海里的工具全在报失真 50 倍的数字——我们已修，但这不足以立身 |
| 摩擦层有厚度 | 3,888 条明确事件、219 条反复签名、机制覆盖 59.3% | 改进闭环的**信号源真实存在** |
| 闭环已走通一次 | 缺口 5→0 → 规则落笔 → 两条 watch 基线冻结（9/12 出判定） | 产品独有的一拍已被真实数据验证可运转 |
| 资产墙无话可说 | 872/883 无相关任务记录 | 资产层的正确形态不是"墙"，是**被闭环调用的生命周期机制**（staleness/deprecation 正是综述点名的空位） |
| 纵向测量今天就成立 | "先读后写"机制周曲线：W28 4 次 → W31 141 次（重舰队周）→ 规则写入后 W34 1 次 | **遵守率曲线可以直接从现有表算出来**，不需要新采集 |

## 3. 威胁面（诚实评估）

| 威胁 | 概率 | 应对 |
| --- | --- | --- |
| Anthropic 把分析做进 harness | 用量/组织报表**已经做了**；但深入本地摩擦签名+规则验证与其云/企业路线相反，短期不重叠 | 把差异钉在"纵向+跨 harness+本地闭环"，不与官方报表正面竞争 |
| 转写格式漂移 | 持续发生（本轮就补了 teammate-message） | 准确性门禁（0 mismatch 对账）恰是护城河：商品化工具解析错了没人知道，我们错了当天红 |
| 浅层红海挤占注意力 | 高 | 明确不做：成本报表深化、通用 trace 平台化 |

## 4. 方向定夺

**放弃的两条**：
- ~~成本/用量深化~~：红海 + 厂商占位，工作 token 换位已拿走这层 90% 的价值；
- ~~通用可观测平台化~~：需要 SDK 接入，等于放弃零接入护城河，进别人的主场。

**核心方向（一条主线两翼）**：

> **主线 · 经验层 CI**：把"摩擦签名 → 简报 → 用户写规则 → 签名验证"扩成完整生命周期——
> 每条规则/技能用**你自己的历史**回答四个问题：被提到了吗（coverage，已有）、
> 被遵守吗（adherence 曲线，§2 已证可算）、还有用吗（staleness，资产层机制转正）、
> 该退役吗（deprecation，带回滚清理已建成）。这是研究综述点名无人做、
> 公开讨论只有静态测量、而我们独有纵向真实数据的一层。

> **翼一 · 舰队作业面**：闭环的读数以舰队为单位呈现——"这周的舰队比上周省了吗、
> 少卡了吗、哪条规则的曲线降了"。Agent Teams 官方化让这个人群快速变大；
> 本周落地的舰队汇总/现在视图/循环徽标就是这一翼的地基。

> **翼二 · 事实层存档（护城河声明）**：harness 30 天删证据，Flatline 的库就是唯一副本。
> 做两件便宜的事把它变成显性价值：数据页展示"已抢救的转写"（源文件已被 harness 删除、
> 库里仍完整的会话数——纯事实）；导出路径落文档。跑得越久越不可替代。

## 5. 落地拍（P17，按依赖排序）

| 拍 | 内容 | 依赖 |
| --- | --- | --- |
| P17-1 | **规则遵守率曲线**：机制关键词 → 对应签名的周频次/会话占比曲线，挂在规则资产页与摩擦签名页；判定句"这条曲线是对齐不是因果" | 无（§2 已证可算） |
| P17-2 | **watch 第一批判定落地复盘**（9/12 前后）：verified/no_change 的真实案例写进 dogfood，作为闭环的第一个完整证言 | 等时间 |
| P17-3 | **已抢救转写计数**：native_files 中源文件已不存在但事件仍在库的会话数，数据页一行事实 + 导出说明 | 无 |
| P17-4 | **资产层机制转正**：staleness/deprecation 从"墙"改为闭环的下游动作——休眠技能出现在简报/清理建议里，而不是等人来看墙 | P17-1 |
| P17-5 | **舰队周对比**：`/fleet` 加 compare（同口径上一周期），"这周的舰队省了吗"一屏回答 | 已有 compare 机制 |
| 首启假 dormant 修法 | 需 ADR（`pending_history` 独立态 vs 墙上标注），见回测记录 §4 | 用户拍板 |

## 6. 一句话定位（对外可说的版本）

> **Flatline：你的 agent 工作方式的 CI。**
> harness 删掉的历史它存着，别人猜的"规则有没有用"它用你自己的曲线证明。

---

### 调研来源（2026-08-29）

- 本地工具红海：ccusage / ccflare / Sniffly / Claude-Code-Usage-Monitor / CCHV（toriihq.com、dev.to、claudefa.st、easyclaw.com 综述）
- 厂商动向：Anthropic Analytics API 与企业仪表盘、OTel 集成（eesel.ai、dash0.com、AWS 博客）
- 研究综述：Dynamic Agent Skills lifecycle survey（arXiv 2607.10113，124 篇审计："no system addresses the full lifecycle"）、Experience Compression Spectrum（arXiv 2604.15877）
- 规则遵守测量：0/524 无文件基线、5.6%/函数衰减（alexdunlop.com 证据综述、dev.to 基准文章、cc-audit）
- 留存痛点：anthropics/claude-code#59248、#62476、#23710（30 天静默删除与 `0` 值 bug）
- 舰队趋势：Agent Teams 官方化 2026-02（tembo.io、shipyard.build、developersdigest.tech）
