# Flatline

> **Your agent skills fail silently. Flatline notices.**

Flatline 是一台**本地优先**的 Agent 资产生命体征监护仪：它持续追踪每个 Skill、AGENTS.md、Rule、Hook 在真实工作会话中的生命体征，在资产静默失效、损坏或被绕行时告警，帮助用户诊断、修复或清理，并在资产复活的那一刻确认修复生效。

监护仪，不是驾驶舱：安静、可信、平时不打扰、响的时候一定有事、每个读数都能解释。

## 产品核心

- **静默失效检测**：没有报错、没有异常，资产只是不再参与。Flatline 为每个资产建立来自其自身历史的基线，识别"该出现却没出现"。
- **四拍核心流程**（全部事件驱动，不等统计窗口）：持续监护 → 状态迁移告警 → 诊断 → 处置（修/删/归档/忽略）→ 复活确认（单事件）。
- **生命体征状态**：healthy / degraded / silent / broken / bypassed / dormant / no_opportunity / unobservable / awaiting_resurrection / archived。
- **四个 detector**：沉默判定、引用体检、调用后违背（invoked-then-violated）、休眠识别。
- **删除是一等结局**：清理休眠资产与修复沉默资产同等重要；清理收益确定性可计算，且任何清理可回滚。

## 目标架构

- **Go** 单二进制；
- **纯 Go SQLite 驱动**（无 CGO 依赖）；
- **单 daemon**：唯一数据属主，增量摄取 Session 与资产快照，驱动状态机；
- **loopback**：本地 API 只监听回环地址，不上传任何数据，不依赖登录；
- **内嵌 SPA**：Web UI 以 embed 方式打包进单二进制，三页信息架构（生命体征 / 会话 / 变化时间线）。

```
Source Adapters → Canonical Event Store ─┐
Asset Snapshotter → Asset Registry ───────┼→ Effective Bundle Resolver
                                          ├→ Opportunity & Participation Tracker
                                          ├→ Reference Health Checker
                                          ├→ Bypass Detector
                                          └→ Vital State Machine → Alerts → Local API → Web UI / CLI
```

## MVP 范围

**包含**：CC + Codex 适配器；资产快照与版本；机会/参与/基线追踪；四个 detector；生命体征墙 + 诊断页 + 变化时间线；四种处置 + 复活确认；批量清理流；Session 基础浏览；全链路观测等级与 locator 展示。

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
| [docs/roadmap.md](docs/roadmap.md) | 分阶段路线图（P0–P7，含目标/交付物/验收标准/依赖/并行性） |
| [docs/adr/](docs/adr/) | 架构决策记录 |
| [AGENTS.md](AGENTS.md) | Agent 执行规范（证据纪律、写入纪律、交付规范） |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献流程（分支、提交、审查清单） |
| [DEVELOPMENT.md](DEVELOPMENT.md) | 开发指南（构建、测试分层、质量门禁） |

`docs/flatline-prototype.zip` 为 UI 原型设计输入，只读保留，不解压到生产目录。

## 状态

项目已完成 P0、P1，并已完成 P2 事实层与 Source Adapters：canonical event store、CC/Codex 适配器、合成 fixture、字段矩阵、locator 下钻、增量幂等和 EnvironmentChanged 锚点均已通过测试。Go 质量门禁（`gofmt` / `go vet` / `go test` / `go build`）已通过；P3 尚未开始。

## 许可

待定（发布前确定）。