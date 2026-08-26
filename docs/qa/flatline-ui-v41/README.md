# Flatline UI v41 对照证据

本目录保存本轮真实本地数据 UI 的截图、原型参考图、图标 inventory、API 事实摘要和交互验证结果。截图由本地 daemon `127.0.0.1:18899` 生成，未使用演示 fixture。

## 截图矩阵

`current/` 包含 8 个路由 × `zh/en` × `light/dark` 共 32 张截图：

- `wall`：资产墙
- `sessions`：会话列表
- `session-detail`：会话详情
- `asset-related`：有相关任务证据的资产详情
- `asset-no-opportunity`：没有相关任务记录的资产详情
- `timeline`：变化时间线
- `stats`：统计
- `cleanup`：清理候选

`reference/` 保存从 `docs/flatline-prototype.zip` 只读审计副本中取得的原型参考图。原型源码审计副本位于临时目录 `/tmp/flatline-prototype-src.ko1X6F/`，没有解压到生产 `web/` 目录。

## 对照结论

- 原型运行时实际使用的 73 个 Lucide 图标 path 与当前 `app.js` icon inventory 逐项一致；别名 `kebab-case` / `camelCase` 已逐项比对，没有缺失项。
- 会话详情使用原型中的原始 `⊟` / `⊞` 回合、调用折叠符号；没有为该位置新增外部链接图标。
- 参与率只显示“已记录参与 / 已记录相关任务”的分子分母；没有相关任务分母时显示“未记录”，不显示 0%、圆环或伪造曲线点。
- 参与率曲线的空桶保留为 `null`；单点真实数据只显示单点，不跨越缺失区间连接曲线。
- v41 浏览器验证覆盖主题、语言、内部滚动、对话/轨迹、回合折叠、调用筛选、右侧 inspector 页签和搜索浮层。

## 真实数据摘要

- 资产：153
- 会话：874
- 有相关任务证据的资产：7
- 没有相关任务记录的资产：146
- 相关任务记录：136
- 参与记录：136
- 原生历史采集：1371 个文件读取 1371 个；5 个原始损坏 subagent JSONL 仅记录 warning，不进入伪造数据路径。

完整截图元数据与交互结果见 [`data/manifest.json`](data/manifest.json)，原型图标 inventory 见 [`data/prototype-icon-inventory.json`](data/prototype-icon-inventory.json)。
