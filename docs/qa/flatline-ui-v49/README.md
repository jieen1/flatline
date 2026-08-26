# Flatline 原型对照与真实数据验收（v82）

本目录保存本轮最终验收证据。原型输入仍是只读的 `docs/flatline-prototype.zip`；对照使用原型内的 `Flatline.dc.html`、设计系统 token、组件样式和原型截图，不把原型源码解压到生产 `web/` 目录。

## 验收范围

- 页面矩阵：资产墙、会话列表、会话详情、有关联任务的资产详情、没有相关任务记录的资产详情、变化时间线、统计、清理。
- 状态矩阵：中文/English × 浅色/深色，共 32 张首屏截图。
- 资产详情交互：诊断、原文、版本；当前真实数据库没有处置记录，因此没有伪造“处置历史”标签或内容。
- 会话详情交互：对话/轨迹、回合、调用、回合折叠与恢复；切换后使用同一真实会话的事件集合重新渲染。
- 时间线交互：全部、状态迁移、资产变更、环境变化筛选。
- 主题和语言：点击操作后检查 DOM 状态、`localStorage` 持久化和页面语言。
- 滚动：检查页面外层 `screen-scroll`；会话详情按原型固定外层画布，并检查事件流、记录详情、对话流等内部滚动容器实际可滚动。

## 结论摘要

- 设计系统 token 对照：`light.missing=[]`、`light.mismatched=[]`、`dark.missing=[]`、`dark.mismatched=[]`，`exact=true`。
- 原型图标闭合集合：61 个（包含原型静态源码和运行时绑定图标）；32 个页面状态均未发现越界或自定义图标，`iconPathMismatches=[]`。会话详情的“回合”使用 `rows-2`/`rows-3`， “调用”使用 `list-collapse`/`list`，折叠状态会随操作切换；三个工具栏图标均按原型实际尺寸 14×14 渲染。
- 英文静态 UI：每个英文页面仅保留语言切换控件“中文”这一项中文；真实会话标题、任务文本、工具输入输出、资产原文和定位信息属于原始证据，保持原文，不被翻译器改写。
- 真实数据审计：`real-data-audit.json` 的 `allPassed=true`；本次 daemon 快照为 932 个资产、938 个版本、877 个会话、426,215 个事件、311 个相关任务机会和 339 条参与记录。其中 780 个资产来自只读的 `~/.codex` 扫描；关联资产详情实测返回 111 个关联会话，其中 95 个关联会话带有真实任务标题或 task text。审计只保存计数、类型、状态和证据存在性，不保存会话标题、任务文本、路径、定位器或 transcript 内容。
- 缺失语义：没有相关任务记录的资产不显示伪造的 0% 圆环或合成曲线；分子、分母、sparkline 和漏斗步骤保持“未记录”。

## 截图索引

每组均包含 zh/en 与 light/dark 四种状态。

本轮最终原型对照截图集中在 [prototype-parity-v62](./prototype-parity-v62/)：其中 `current-*` 是真实 daemon 数据渲染，`prototype-*` 是原型对应状态。真实数据的行数和文本允许不同，壳层、token、组件结构、图标语义和交互状态必须一致。

- 资产墙：[wall-zh-light.png](./wall-zh-light.png)、[wall-zh-dark.png](./wall-zh-dark.png)、[wall-en-light.png](./wall-en-light.png)、[wall-en-dark.png](./wall-en-dark.png)
- 会话列表：[sessions-zh-light.png](./sessions-zh-light.png)、[sessions-zh-dark.png](./sessions-zh-dark.png)、[sessions-en-light.png](./sessions-en-light.png)、[sessions-en-dark.png](./sessions-en-dark.png)
- 会话详情：[session-detail-zh-light.png](./session-detail-zh-light.png)、[session-detail-zh-dark.png](./session-detail-zh-dark.png)、[session-detail-en-light.png](./session-detail-en-light.png)、[session-detail-en-dark.png](./session-detail-en-dark.png)
- 有关联任务资产详情：[asset-related-zh-light.png](./asset-related-zh-light.png)、[asset-related-zh-dark.png](./asset-related-zh-dark.png)、[asset-related-en-light.png](./asset-related-en-light.png)、[asset-related-en-dark.png](./asset-related-en-dark.png)
- 没有相关任务记录资产详情：[asset-no-opportunity-zh-light.png](./asset-no-opportunity-zh-light.png)、[asset-no-opportunity-zh-dark.png](./asset-no-opportunity-zh-dark.png)、[asset-no-opportunity-en-light.png](./asset-no-opportunity-en-light.png)、[asset-no-opportunity-en-dark.png](./asset-no-opportunity-en-dark.png)
- 变化时间线：[timeline-zh-light.png](./timeline-zh-light.png)、[timeline-zh-dark.png](./timeline-zh-dark.png)、[timeline-en-light.png](./timeline-en-light.png)、[timeline-en-dark.png](./timeline-en-dark.png)
- 统计：[stats-zh-light.png](./stats-zh-light.png)、[stats-zh-dark.png](./stats-zh-dark.png)、[stats-en-light.png](./stats-en-light.png)、[stats-en-dark.png](./stats-en-dark.png)
- 清理：[cleanup-zh-light.png](./cleanup-zh-light.png)、[cleanup-zh-dark.png](./cleanup-zh-dark.png)、[cleanup-en-light.png](./cleanup-en-light.png)、[cleanup-en-dark.png](./cleanup-en-dark.png)
- 本轮交互与缺失态补充截图：[会话轨迹](./current-session-trajectory-light-v81.png)、[会话对话](./current-session-chat-light-v81.png)、[无相关任务浅色](./current-asset-no-opportunity-light-v82.png)、[无相关任务深色](./current-asset-no-opportunity-dark-v82.png)、[关联任务资产](./current-asset-related-light-v82.png)。

资产详情状态另存为：[诊断](./asset-related-diagnosis-zh-light.png)、[原文](./asset-related-source-zh-light.png)、[版本](./asset-related-versions-zh-light.png)。状态清单见 [asset-tabs-manifest-v61.json](./asset-tabs-manifest-v61.json)；本轮整体页面截图和结构报告以 v82 重新生成的 `prototype-parity-v62/` 为准。

## 机器审计证据

- [final-dom-audit.json](./final-dom-audit.json)：8 个路由 × 2 语言 × 2 主题，共 32 个状态；图标、英文 UI、外层/嵌套滚动容器和 CSS 关键 token。
- [final-dom-audit.mjs](./final-dom-audit.mjs)：审计规则；英文审计排除“中文”语言切换控件，原始证据节点保留源语言。
- [token-parity-audit.json](./token-parity-audit.json)：从原型设计系统 token 文件读取并对比当前 SPA 的 light/dark 值。
- [real-data-audit.json](./real-data-audit.json)：只读 API 真实数据审计，包含关联任务、无相关任务、会话事件/回合/原始定位信息和时间线事实。
- [interaction-audit-v62.json](./interaction-audit-v62.json)：真实会话上的轨迹/对话、回合/调用折叠恢复、事件选中、记录详情/生效配置切换、嵌套滚动和侧栏搜索。
- [interaction-audit-v62.mjs](./interaction-audit-v62.mjs)：交互验收规则。
- [prototype-parity-v62/summary.json](./prototype-parity-v62/summary.json)：原型与当前页面的组件计数、图标路径、图标越界和滚动失败汇总；当前 `iconPathMismatches=[]`、61 个原型图标闭合集合无越界，32 个状态无滚动失败。
- [prototype-parity-v62/report.json](./prototype-parity-v62/report.json)：每个路由的结构和几何对照明细，含 light/dark 两套截图。
- [visual-verdict-v82.json](./visual-verdict-v82.json)：本轮截图对照的结构化视觉判定，`score=95`、`verdict=pass`。
- [route-facts-audit-v82.json](./route-facts-audit-v82.json)：`#/assets` 入口的事实投影核验，确认比例与曲线来自完整资产事实而不是轻量摘要。
- [final-capture-manifest-v61.json](./final-capture-manifest-v61.json)：32 张截图路由、语言、主题和截图来源；v82 页面截图位于 `prototype-parity-v62/`。

## 数据差异说明

原型截图中的 24 个资产、41 个会话、密集曲线和完整活动热力图是设计参考状态；本轮页面使用 daemon 当前 SQLite 中的真实本地数据，因此资产数量、会话数量、曲线点数和空状态分布会不同。视觉结构、颜色、图标、布局和交互按原型对照；数据密度不通过补造记录来迎合截图。当前历史处置没有记录，故保持真实空状态。

会话详情外层 `screen-scroll` 显示为固定画布是原型的分栏设计，不代表无法滚动；事件轨迹、事件详情和对话内容分别使用独立滚动容器，交互审计已验证其实际高度超过视口且 `overflow-y: auto`。统计/清理等页面若内容超过视口则由外层页面滚动。

本轮 daemon 读取历史时保留了 5 个格式损坏的 native JSONL 文件告警；这些源文件没有被改写或删除，详见运行日志与交付风险说明。资产详情的关联会话由“机会记录或参与记录”的并集驱动，机会存在但参与未记录的会话不会消失。无相关任务记录的页面不渲染圆形“无法计算参与率”组件；当前 DOM 中不存在 `diagnosis-rate-missing`，漏斗分子/分母保持 null/“未记录”。
