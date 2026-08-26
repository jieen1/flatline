# Flatline UI v42 对照证据

本目录保存最终一轮真实本地数据 UI 的截图、原型参考图、图标 inventory 和交互验证结果。截图由本地 daemon `127.0.0.1:18899` 生成，未使用演示数据。

## 截图矩阵

`current/` 包含 8 个路由 × `zh/en` × `light/dark` 共 32 张截图：资产墙、会话列表、会话详情、有相关任务证据的资产详情、没有相关任务记录的资产详情、变化时间线、统计、清理候选。`reference/` 保存从 `docs/flatline-prototype.zip` 只读审计副本取得的原型参考图。

## 对照结论

- 原型运行时实际使用的 73 个 Lucide 图标 path 与当前 `app.js` inventory 逐项一致；kebab/camel 别名无缺失、无 path mismatch。
- 会话详情使用原型中的原始 `⊟` / `⊞` 回合、调用折叠符号；右上角状态胶囊为纯文字，未加入原型没有的图标；真实 transcript 标题已显示在详情头部，ID 仅作为定位元数据。
- 参与率只显示已记录参与 / 已记录相关任务的分子分母；没有相关任务分母时显示“未记录”，不显示 0%、圆环或伪造曲线点。空桶保留为 `null`，不跨缺失区间连线。
- 浏览器验证覆盖中英、明暗主题、长页面内部滚动、对话/轨迹、回合折叠、调用筛选、右侧 inspector 页签和搜索浮层；32 个路由状态无渲染错误。

## 真实数据摘要

- 资产：153；会话：874
- 有相关任务证据的资产：7；没有相关任务记录的资产：146
- 相关任务记录：136；参与记录：136
- 原生历史采集：1371 个文件读取 1371 个；5 个原始损坏 subagent JSONL 仅记录 warning，不进入伪造数据路径。

完整截图元数据与交互结果见 [`data/manifest.json`](data/manifest.json)，原型图标 inventory 见 [`data/prototype-icon-inventory.json`](data/prototype-icon-inventory.json)，结构化对照结果见 [`data/comparison.json`](data/comparison.json)。
