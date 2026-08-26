# Flatline UI v45 对照证据

本目录保存当前实现与 `docs/flatline-prototype.zip` 中 Flatline 原型的最终一轮对照证据。原型只读审计副本来自 zip，当前截图由本地 daemon `127.0.0.1:18899` 生成，使用本机 SQLite 中已采集的 Claude Code / Codex 原生历史，不使用演示数据。

## 截图矩阵

`current/` 保存 8 个路由 × `zh/en` × `light/dark` 共 32 张截图：资产墙、会话列表、会话详情、有相关任务证据的资产详情、没有相关任务记录的资产详情、变化时间线、统计、清理。`reference/` 保存原型对照图；`data/` 保存运行时 token、图标路径、交互和真实数据摘要。

## 本轮结论

- 运行时 39 条受审计 SVG path（33 条原型静态 Lucide path + 6 条原型运行时动态 path）逐条匹配，未知渲染 path 为 0；Codex 图标单独使用原型同源 brand SVG，不冒充 Lucide。
- 浅色/深色 30 个 CSS token 均与原型运行时一致，token mismatch 为 0。
- 会话详情的“回合 / 调用”使用原型的 `rows-2` / `list-collapse` 路径，折叠后切换为原型的 `rows-3` / `list` 路径；轨迹、事件流、回合折叠、调用筛选、摩擦轴和会话证据上下文均有真实 DOM 变化，具体 SVG path 保存在 `data/interaction-audit.json`。
- 时间线真实 663 条记录可切换为状态迁移 160、资产变更 159、环境变化 344，再恢复全部 663；不是静态按钮。
- 时间线状态徽标来自 API 明确投影的 `state_transitions.to_state`（dormant 7、no_opportunity 146、healthy 7），不从当前资产状态反推历史状态；该边界也有 fixture API 回归断言。
- 搜索面板使用绝对定位，不改变主滚动容器高度；长页面和事件流均有可滚动容器。
- 无相关任务的资产不展示圆形伪百分比；相关任务、参与记录和参与率分母均显示“未记录”，不把缺失转换为 0。

## 真实数据摘要

本轮捕获时：153 个资产、874 个会话、418679 个事件、136 条相关任务记录、136 条参与记录；状态为 7 个健康、146 个没有相关任务记录；来源为 Claude Code 23 个会话、Codex 851 个会话。原生历史扫描读取 1371/1371 个文件，5 个损坏 subagent JSONL 只记为 warning，不进入伪造数据路径。

详细数据见 [`data/real-data-summary.json`](data/real-data-summary.json)，结构化对照见 [`data/comparison.json`](data/comparison.json)，运行时审计见 [`data/runtime-audit.json`](data/runtime-audit.json)，交互审计见 [`data/interaction-audit.json`](data/interaction-audit.json)。

## 已知边界

原型中的示例项目、资产名称、会话数量和成本/token 是设计输入，不应覆盖本地真实数据；因此当前截图中的数据密度与原型示例不同，但颜色、图标路径、结构、状态语义和交互按原型核验。5 个损坏的原生 JSONL 文件仍需上游数据源修复，Flatline 会跳过损坏行并保留 warning。
