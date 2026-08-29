# Flatline

> **你的 agent 会话里发生了什么，哪里反复卡住 —— Flatline 从本机转写里读出来。**

Flatline 是一台**本地优先**的 Agent 工作历史监护仪。它只读本机已有的会话转写，不上传、不需要登录，回答两个主线问题：

1. **这些会话里发生了什么**（会话管理）：每个会话的用户轮次、工具调用、token、时长、层级（主会话 / 子代理）、所属项目与工作树，全部从原文派生并与原文逐字段对得上。
2. **哪里反复卡住**（摩擦发现）：把工具失败、非零退出、用户打断、资产违背归一成**签名**，按签名看它是新出现、还在发生、已经安静，以及哪些反复摩擦是"你写的规则里根本没提到的机制"。

**资产生命体征是下钻，不是入口**（ADR-18）：Skill / AGENTS.md / Rule / Hook 的静默、损坏、绕行、休眠，是从一条摩擦或一个会话往下走一层才看到的东西。

监护仪，不是驾驶舱：安静、可信、平时不打扰、响的时候一定有事、每个读数都能解释。

## 产品核心

- **五个数据源，一份事实**：Claude Code（`~/.claude/projects`）、Codex（`~/.codex/sessions`）、opencode（SQLite）、dsh（zstd 压缩 JSONL，ADR-19）、Hermes。每个源支持哪些字段见 `docs/field-matrix-*.md`；未记录的字段显示"未记录"，不补零。
- **准确性是硬门禁**：会话度量与原始转写逐条对账（`scripts/audit_accuracy.py`），一致性等式（总览 = 会话 = 项目 = 摩擦）由 `internal/api/consistency_test.go` 把关。
- **摩擦签名与生命周期**：同一条摩擦在多少个会话里出现过、第一次和最近一次是什么时候、在最近的窗口里还发不发生。
- **代价与机会**（ADR-20）：从既有事实里投影出有限的几条洞察——中断集中在哪里、哪些会话投入巨大却没有记录到改动、同一失败动作在会话里连撞多少次、哪些文件被反复读取、哪些反复出现的机制你的规则没提到——每条带判定规则原文，可下钻到分子分母。
- **工作 token 领衔**（ADR-25）：`work_tokens = input + output + cache_write`，不含缓存读取。本机缓存读取占总量 98%，总量单独看会把一次运行的代价放大约 50 倍；凡领衔展示 token 的位置（总览 KPI、会话行、详情、数据页）与 `sort=tokens` 排序键均以工作 token 为准，总量与缓存占比降为副行、仍然可见。
- **舰队是一等展示单元**（ADR-25）：本机 69% 的会话是子代理，最大单会话指挥 108 个。`/sessions/{id}/fleet` 把父+子整棵树读时聚合：每个孩子的角色/代价/摩擦、树的 token 四分量、git 结局证据（只说"未见失败"，不猜成功）、同项目上一支舰队并排对照。
- **"现在"视图**：总览首屏列出转写文件 10 分钟内被写过的会话（`/now`，no-store 不可被缓存钉住），带同在写的子代理数与"同一失败 ×N"循环徽标（同签名 60 分钟 ≥5 次，与洞察层同阈值）。安静的机器不渲染该区块。
- **遵守曲线**（P17-1）：规则正文提到的机制 → 其签名的近 12 周频次曲线（周一为界、空周带真实分母出现），挂在规则资产页与摩擦签名简报里。判定句写明只陈述对齐、不判定因果。
- **已抢救转写**（P17-3）：harness 默认 30 天静默删除转写；数据页展示"源文件已删、本库仍完整"的会话计数——跑得越久，这个库越是唯一副本。
- **规则覆盖缺口**：反复出现的 harness 机制，如果你写的 rule / AGENTS.md 里一个字都没提到，页面把它列出来——只陈述"没提到"，不声称"因此才发生"。
- **静默失效检测**：没有报错、没有异常，资产只是不再参与。Flatline 为每个资产建立来自其自身历史的基线，识别"该出现却没出现"。
- **四拍核心流程**（全部事件驱动，不等统计窗口）：持续监护 → 状态迁移告警 → 诊断 → 处置（修/删/归档/忽略）→ 修改后验证（单事件）。
- **生命体征状态**：healthy / degraded / silent / broken / bypassed / dormant / no_opportunity / unobservable / awaiting_resurrection（修改后验证中）/ archived。
- **四个 detector**：沉默判定、引用体检、调用后违背（invoked-then-violated）、休眠识别。
- **删除是一等结局**：清理休眠资产与修复沉默资产同等重要；清理收益确定性可计算，且任何清理可回滚。

## 目标架构

- **Go** 单二进制；
- **纯 Go SQLite 驱动**（无 CGO 依赖）；
- **单 daemon**：唯一数据属主，增量摄取 Session 与资产快照，驱动状态机；
- **loopback**：本地 API 只监听回环地址，不上传任何数据，不依赖登录；
- **内嵌 SPA**：Web UI 以 embed 方式打包进单二进制，五页信息架构（总览 / 会话 / 摩擦 / 资产 / 变化时间线；见 ADR-18）——前四页是主线，资产页是下钻落点。

```
Source Adapters → Canonical Event Store ─┐
Asset Snapshotter → Asset Registry ───────┼→ Effective Bundle Resolver
                                          ├→ Opportunity & Participation Tracker
                                          ├→ Reference Health Checker
                                          ├→ Bypass Detector
                                          └→ Vital State Machine → Alerts → Local API → Web UI / CLI
```

## MVP 范围

**包含**：

- **数据源**：Claude Code、Codex、opencode、dsh、Hermes 五个只读适配器，每个附字段矩阵；源根在数据页里可命名、可关停（`sources` 注册表）。
- **会话管理**：会话列表/检索/分面/导出、会话详情（事件、命令、文件、子会话）、会话层级、项目页、时间统计、版本化的会话度量（token / 时长 / 行数）。
- **摩擦发现**：摩擦分类与签名、签名生命周期（new / active / quiet / once）、提示字典、规则覆盖缺口、摩擦 → 资产证据桥。
- **资产监护（下钻层）**：资产快照与版本、机会/参与/基线追踪、四个 detector、生命体征墙 + 诊断页 + 变化时间线、四种处置 + 修改后验证、批量清理流。
- **贯穿**：全链路观测等级与 locator 展示；每个比例可下钻到分子分母。

**不包含**（明确不做，见 `docs/roadmap.md` 与系统设计 §8）：

- 账户 / 云同步 / 团队协作；
- AI 分析；
- 桌面壳；
- 插件运行时；
- 群体参考（匿名群体参考区间）；
- 正式统计改善轮次（ImprovementCycle，整体移入 Backlog，UI 与代码均不实现）。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/flatline-system-design-v0_4.md](docs/flatline-system-design-v0_4.md) | 核心系统设计（产品定义、状态机、架构、数据模型、MVP 范围、验收标准、ADR） |
| [docs/flatline-ui-design-guidelines-v2_0.md](docs/flatline-ui-design-guidelines-v2_0.md) | UI 设计思路与原型指南（信息架构、组件规格、文案规则、验收清单） |
| [docs/flatline-session-first-redesign-v1.md](docs/flatline-session-first-redesign-v1.md) | 会话优先重构（性能根因与实测、会话管理/总览/摩擦分类的数据层、各阶段实施补充）。**当前 API 契约以 §27 总表为准**，§1–§26 保留为历史 |
| [docs/flatline-friction-page-design-v1.md](docs/flatline-friction-page-design-v1.md) | 摩擦页信息架构与交互 |
| [docs/flatline-direction-2026-08.md](docs/flatline-direction-2026-08.md) | 后续核心方向（2026-08-29 调研定夺：经验层的 CI · 舰队作业面 · 事实层存档） |
| [docs/roadmap.md](docs/roadmap.md) | 分阶段路线图（P0–P7 + 会话优先重构 P8–P15 + P16/P17 落地拍） |
| [docs/adr/](docs/adr/) | 架构决策记录（ADR-1..26；近期：18 摩擦优先、25 舰队汇总与工作 token、26 首评落在证据上） |
| [docs/field-matrix-claudecode.md](docs/field-matrix-claudecode.md) · [codex](docs/field-matrix-codex.md) · [opencode](docs/field-matrix-opencode.md) · [dsh](docs/field-matrix-dsh.md) | 每个数据源支持 / 不支持 / 未记录哪些字段 |
| [AGENTS.md](AGENTS.md) | Agent 执行规范（证据纪律、写入纪律、交付规范） |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献流程（分支、提交、审查清单） |
| [DEVELOPMENT.md](DEVELOPMENT.md) | 开发指南（构建、测试分层、质量门禁） |

`docs/flatline-prototype.zip` 为 UI 原型设计输入，只读保留，不解压到生产目录。

## 常驻运行（存档承诺的运维面）

harness 默认 30 天删除转写，抢救者必须一直在场。安装为 systemd 用户服务（开机自启、崩溃自拉）：

```bash
./bin/flatline service install    # 写入 ~/.config/systemd/user/flatline.service 并启用
./bin/flatline service status     # 查看
./bin/flatline service uninstall  # 移除
```

WSL / 纯 SSH 登录下用户级 systemd 实例可能未启动，install 会写好单元并打印精确的一次性修复命令
（`sudo loginctl enable-linger <user>`）。服务以 `-asset-scope user` 运行：后台守护没有"当前项目"，
不会把 $HOME 当项目根去扫。ExecStart 指向安装时的二进制路径，重新构建后 `systemctl --user restart flatline` 生效。

## 状态

**P0–P6（资产监护链路）**：canonical event store、适配器、真实本地历史只读摄取、字段矩阵、locator 下钻、增量幂等、资产快照与版本、EffectiveBundle、机会/参与/基线、四个 detector、状态机、loopback API、内嵌 SPA、通知、显式处置、修改后验证、批量清理与变化时间线，均已通过自动化测试。若原生会话没有提供可验证的任务形状，界面明确显示"没有相关任务记录"，不会把会话存在伪装成机会。

**P8–P13（2026-08 会话优先重构）**：会话管理、总览"这段时间"、摩擦分类与签名生命周期、五源覆盖、准确性门禁、摩擦 → 资产证据桥已落地，实测记录见 `docs/qa/dogfood-2026-08-22.md`，逐项验收见 `docs/roadmap.md` 的"2026-08 会话优先重构"一节。

**2026-08-29（全量跑通 + 方向定夺，ADR-25/26）**：在无 Go 环境的机器上从零跑通并用系统自查自修——
`<teammate-message>` 计数修正（此前 26.7% 的"用户轮"是 agent 间消息）、启动重算移到监听之后
（API 全黑 302s → 0s）、舰队汇总、"现在"视图、工作 token 全面换位、遵守曲线、
首启"假 dormant"修复（ADR-26，全新回放验证 dormant 中停 12→0）。P7 真实历史回测完成"安静"一半：
895 条迁移逐条核验、零误报、证据可下钻至原文（`docs/qa/backtest-2026-08-29.md`）。
后续核心方向定夺为**经验层的 CI**（`docs/flatline-direction-2026-08.md`）。

**仍在等待**：告警态（silent/broken）在真实历史上零触发，其准确率待第一个真实案例；
两条 signature watch 的首批 verified/no_change 判定约 2026-09-12 产出。

## 许可

待定（发布前确定）。
