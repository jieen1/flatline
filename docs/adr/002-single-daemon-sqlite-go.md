# ADR-2: 单 daemon + SQLite + Go 单二进制

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline 设计（系统设计 v0.4，沿用 v0.3）

## 背景

Flatline 需要持续摄取本地 Session 历史、维护资产快照与状态机，并对外提供查询界面。运行形态必须与本地优先（ADR-1）一致：无外部服务、无网络依赖、部署即一个可执行文件（系统设计 v0.4 §6；roadmap P1）。

## 决策

采用 Go + 纯 Go SQLite 驱动（如 `modernc.org/sqlite`）+ 单 daemon 进程 + loopback HTTP API + 内嵌 SPA 的单二进制形态；daemon 是唯一数据属主，UI 与 CLI 只通过本地 API 访问数据。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 单 daemon + SQLite + Go 单二进制（本决策） | 无 CGO 依赖、跨平台单文件分发；daemon 单一数据属主避免并发写冲突 | 单进程承载摄取与派生层，需内部隔离 | 采纳 |
| 多进程（采集器 + 服务分离） | 职责分离 | 增加 IPC 与部署复杂度，违背"最小可运行形态" | 拒绝 |
| 非 Go 语言 + 系统 SQLite | 生态成熟 | CGO/动态库破坏单二进制分发 | 拒绝 |

## 后果

- 正面：`go build` 产出单二进制（roadmap P1 验收）；纯 Go 驱动无网络/CGO 依赖，符合隐私边界。
- 负面：daemon 崩溃即服务不可用；派生层（tracker、detector、状态机）必须保持可重算（见 ADR-10），避免单点状态腐化。
- 对 MVP 边界：桌面壳（Electron 等）不进入 MVP，Web UI 由单二进制内嵌提供。