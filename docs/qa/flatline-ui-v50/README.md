# Flatline UI 质量基线 v50

这一轮取代 `docs/qa/flatline-ui-v45/` 作为 UI 质量基线。v45 面向的是「资产优先」的旧信息架构，页面矩阵是资产墙 / 会话 / 时间线 / 统计 / 清理；本轮的信息架构已经换成「会话与摩擦优先」，页面对不上了，所以重开一份。

基线的作用：把「这套界面在真实数据上长什么样、哪些东西必须为零、哪些组件在哪些页面出现」固定成可以重跑的证据，下一轮改动拿它对照。

## 本轮的信息架构

侧栏五个入口，加上四个从入口点进去的详情页，一共九个路由。

| 路由 | 地址 | 回答的问题 |
| --- | --- | --- |
| 总览 | `#/` | 这段时间在哪些项目、摩擦集中在哪 |
| 会话 | `#/sessions` | 找一次具体的会话，按项目 / harness / 标签 / 时间筛 |
| 摩擦 | `#/friction` | 哪些失败在反复发生，按项目 × harness / 类别 / 工具 / 签名分组 |
| 资产 | `#/assets` | 哪些 AGENTS.md / SKILL.md / Rule 需要注意 |
| 变化时间线 | `#/timeline` | 状态迁移、资产变更、环境变化按时间排开 |
| 数据 | `#/stats` | 库健康、数据源管理、导入导出 |
| 会话详情 | `#/sessions/{id}` | 一次会话里究竟发生了什么（轨迹 / 对话 / 命令与文件） |
| 项目页 | `#/projects/{key}` | 一个项目自己的会话、周趋势、摩擦分布、热点文件 |
| 摩擦详情 | `#/friction/{project}/{harness}` | 某个项目 × harness 的摩擦记录逐条与证据 |

## 用的真实数据

daemon 跑在 `127.0.0.1:8809`，数据库是 `scratchpad/p4.db` 的副本 `b7.db`，只读扫描本机五个来源。截图时的快照：

- 会话 1169、事件 407461、资产 931、`data_version` 27，`/api/v1/ingest/status` 为 `ready`。
- 五个来源：Claude Code 225、Codex 879、opencode 51、DeepSeek Harness 14、Hermes 0（探测到根目录但没有会话，页面照实写「无会话」）。
- 总览默认口径是「主会话 · 非空」，`scope.key = main_non_empty`；子代理会话 631 个、空会话 19 个另计，这句话挂在总览标题旁的口径徽标上。

没有造过一条记录。截图里的中文会话标题、任务文本、工具输入输出都是本机真实 transcript 的原文，页面按原文显示，不交给界面翻译器改写。

## 截图方法

36 张：9 个路由 × 中文/English × 浅色/深色，全部 1600×1100、`deviceScaleFactor=1`。

无头 Chrome 走 CDP：

1. `Page.addScriptToEvaluateOnNewDocument` 预置 `localStorage` 的 `flatline-locale` 与 `flatline-theme`（页面就是从这两个键读偏好的）；每次只保留一份预置脚本，换状态时先 `Page.removeScriptToEvaluateOnNewDocument` 移掉上一份。
2. 每次访问先导航到 `about:blank` 再导航到目标地址。**这一步是必须的**：只改 hash 的导航不会创建新文档，预置脚本就不会再执行，页面会一直用第一次加载时的语言和主题。本轮第一次跑截图正是踩了这个坑——36 张图全是中文浅色，`document.documentElement.lang` 全都是 `zh-CN`——发现后重跑了整套。
3. 等 `#flatline-screen` 有内容、骨架屏消失，再 `Page.captureScreenshot`。

图在 [current/](./current/)，文件名是 `{路由}-{语言}-{主题}.png`。每张图的路由、语言、主题和当时的 daemon 数据摘要在 [data/manifest.json](./data/manifest.json)。

## 核对结论

### 必须为零的六项：36 个状态全部为零

[data/quality-checks.json](./data/quality-checks.json)，`summary.all_zero = true`。

| 项 | 含义 | 36 个状态合计 |
| --- | --- | --- |
| `selects` | 原生 `<select>` 元素 | 0 |
| `imgs` | `<img>` 元素 | 0 |
| `dateInputs` | 原生 `<input type=date>` | 0 |
| `decorativeGlyphs` | 页面自己写的 UI 文案里的装饰符号 | 0 |
| `consoleErrors` | console 报错、未捕获异常、error 级日志 | 0 |
| `non2xxAPI` | `/api/` 的 4xx/5xx 响应 | 0 |

两处判定口径写清楚，免得把正常情况当成失败：

- `decorativeGlyphs` 只数页面自己写的文案。真实 transcript 里的箭头是开发者自己敲进 commit message 的，不是界面装饰，所以证据文本单独记成 `evidenceGlyphs`（本轮 12 处），用的是页面翻译器自己的 skip 选择器来区分，不另立一套标准。
- `non2xxAPI` 只数 4xx/5xx。304 是条件请求命中缓存，是这套接口本来就该有的行为，单独记成 `cache304`（本轮 4 次）。

另外两项也是零：36 个状态没有一个横向溢出，没有一个空图标槽。

### 页内更新不重建：4 处采样全部通过

同一路由内的更新走 morphdom 原地打补丁，不重画整页。采样的三条判据：

1. `#flatline-screen` 和这一页不该变的外壳节点，动作前后仍是同一批对象；
2. 整个动作窗口逐帧采样，`opacity` 恒为 1；
3. `#flatline-screen` 的 `data-entering` 全程不出现 `true`，被更新的那块内容区里没有动画在播。

标记用的是 JS 属性不是 DOM 属性：morphdom 会把新标记里的属性覆盖到它保留下来的节点上，属性标记即使节点被复用也会被擦掉，会误判成重建；JS 属性只在节点对象本身活下来时才在。

| 采样 | 外壳复用 | opacity 恒 1 | 无入场重放 | 数据行 |
| --- | --- | --- | --- | --- |
| 会话页切换排序 | 是 | 是 | 是 | 0/50 复用 |
| 摩擦页切换分组 | 是 | 是 | 是 | 0/11 复用 |
| 总览展开「更多」 | 是 | 是 | 是 | 7/7 复用 |
| 总览收起「更多」 | 是 | 是 | 是 | 7/7 复用 |

数据行换不换不参与判定，因为它取决于行代表的数据换没换：换排序要向 daemon 重新取一页，取回来本来就是另一批会话；换分组维度，项目行变成工具行，行本身就是另一回事。总览展开「更多」不动上面的 KPI，所以七个指标节点原样留在原地。

会话页切换排序期间列表里放的是骨架行，`fl-skeleton-sweep` 的微光会被采到——那是明确的加载态，不是入场动画，单独记，不计入判定。

### 组件清单：11 个组件全部在真实页面上观测到

[data/component-inventory.json](./data/component-inventory.json)。静止状态的组件从上面 36 个状态的审计里取；三个只在特定时刻存在的组件另外去逼出来。

| 组件 | 出现的页面 |
| --- | --- |
| `fl-select` | 会话、摩擦、数据 |
| `fl-filter` | 会话、摩擦、摩擦详情 |
| `fl-segment` | 总览、会话、摩擦、项目页 |
| `fl-tabs` | 会话详情 |
| `fl-chip` | 会话、摩擦 |
| `fl-check` | 会话、数据 |
| `fl-daterange` | 总览、会话 |
| `fl-popover` | 总览、会话、摩擦、摩擦详情、数据 |
| `fl-kpi` | 摩擦、摩擦详情 |
| `fl-skeleton` | 总览、会话、资产、时间线、数据、会话详情、项目页 |
| toast | 数据 |

怎么逼出来的：

- `fl-popover` 逐页点开该页真实存在的每一种触发器（`fl-select` / `fl-filter` / `fl-daterange`）。
- `fl-chip` 只在筛选生效后才存在，所以带着页面自己给出的筛选链接进入。
- `fl-skeleton` 活在路由切换到数据到达之间。在回环 daemon 上这个窗口只有几毫秒，所以给接口加 600ms 延迟再在窗口里采样。
- toast 用数据页的「导出统计快照」触发——那是唯一一个不弹确认框、也不写任何东西的动作。

### 图标：封闭集合没有被突破

[data/icon-inventory.json](./data/icon-inventory.json)，与 [../icon-additions.json](../icon-additions.json) 对齐。

白名单 49 个（v45 的原型闭合集合，加上 B4 记录在案的 `pin` / `tag` / `plus`，再加上 B8 记录在案的 `list-filter` / `refresh-cw`）。36 个状态一共画出 36 个图标，**没有一个越出白名单**，也没有一个空图标槽，`icon()` 解析不出来的名字 **0 个**。

B7 拍这一轮时是 34 个图标、两个名字解析不出来：`list-filter`（1 处，筛选按钮）和 `refreshCw`（6 处，各页的重新扫描 / 重试按钮）。两者都不是越界，是漏画——`icon()` 先把名字 kebab 化再拿去 `prototypeIconNames` 里查，查不到就返回空字符串，连 `.fl-icon` 这个槽都不生成，所以「空图标槽」那一项也数不出它们。B8 按 B4 的流程补齐：SVG 本来就在 `ICONS` 表里（`listFilter` / `refreshCw` 两个驼峰键），缺的是 kebab 别名、白名单条目和 `iconAliases` 里的驼峰映射，三处补齐后七个按钮都画出了图形。理由逐条记在 [../icon-additions.json](../icon-additions.json)。

> `current/` 里的 36 张图是 B7 拍的，那时这两个按钮还没有图形。B8 的前后对比在 [b8-fixes/](./b8-fixes/)。

### 数字与「未记录」的一致性

[data/number-consistency.json](./data/number-consistency.json)，只核对不改 Go。

- 指标值与它下面那句分子分母的说明互相不矛盾：总览与项目页的 KPI 共 4 组（两页 × 中英），`metric_pair_violations = 0`。一个写着「未记录」的数字底下不会跟着一句「3 / 17 个会话……」。
- 总览的五个可核对数字与 `/api/v1/overview` 的载荷逐个对上：会话 114（114 / 1169）、活跃项目 17（17 / 26）、工具调用 89819、摩擦事件 1817（79 / 114 个会话有摩擦）、已记录时长的分母 114 / 114。`api_value_mismatches = 0`，`api_ratio_mismatches = 0`。
- 缺失措辞跟着页面语言走：中文页只出现「未记录」，英文页只出现 “Not recorded”，`wrong_language_hits = 0`。英文页上唯一的汉字是语言切换控件自己的「中文」字样，那是它该有的标签。
- 但 daemon 自己写的解释只有中文一种：摩擦机制词典、匹配到的规则、每块的口径 note。页面是有意把它们标成 `data-no-translate` 当证据看待的，所以英文页上会读到中文——本轮 14 处（总览 7、项目页 6、数据页 1）。这是 daemon 侧的事实，记录不改。**这一条 B8 之后已经不成立了**：这 14 处逐个复核过，全部本来就在 `data-no-translate` 里（不存在前端漏标）；A9 给每条解释配了 `*_en`，英文页改成优先读它，14 处全部变成英文，没有 `*_en` 的记录才照原样印中文并打一个 `zh` 角标。见下面的 B8 一节。

### 总览与项目页的 KPI 行

[data/kpi-row-geometry.json](./data/kpi-row-geometry.json)。总览与项目页各有一行七个 KPI，在 1280 / 1440 / 1600 / 1920 四个宽度下量：12 个七指标行，**没有一个落单**，全部带着尺寸容器。1280 / 1440 / 1600 排成 4 + 3，1920 排成一行 7。

### 总览口径徽标读的是 `scope.key`

[data/scope-key-proof.json](./data/scope-key-proof.json)。`/api/v1/overview` 的 `scope` 是一个对象，徽标原本拿整个对象跟字符串 `"all"` 比，那个分支永远进不去。改成读 `scope.key` 之后，用 CDP 在响应阶段把 `scope.key` 改写成 `"all"` 再交给页面（daemon 和数据库都没动），徽标从「主会话 · 非空」翻成「全部会话」，底下那句完整口径也跟着换。

## B8：这一轮记下的六处视觉问题已经改掉

`current/` 里的 36 张图是 B7 拍的，是这六处问题**修之前**的样子；这一节说的是修之后。前后对比在 [b8-fixes/](./b8-fixes/)，文件名是 `{问题}-{语言}-{主题}-{before|after}.png`。改动只在 `internal/web/static/`，没有动 Go，数据库和接口返回的数字都没变。

复核用的是同一套方法：daemon `127.0.0.1:8810`，数据库是 `scratchpad/p4.db` 的副本 `b8.db`（1171 会话、408058 事件），9 路由 × zh/en × light/dark 共 36 个状态重跑一遍。

| # | 原来读成什么 | 现在怎么画 | 证据 |
| --- | --- | --- | --- |
| 1 | 筛选按钮和六个重新扫描 / 重试按钮上没有图形，只剩文字 | `list-filter` / `refresh-cw` 进白名单与别名表，七个按钮都有图形 | `i1-filter-btn-*`、`i1-rescan-btn-*` |
| 2 | 资产墙 sparkline 在样本很少时是一块灰方块 | 样本 < 5 不画面积、不画中位基线，改画样本点；「未记录」的灰带从满高 34px 收成贴底 3px | `i2-spark-*`（light / dark 各一） |
| 3 | 项目页「周趋势」只有一个周桶时，一根柱子占最左、右边九成空白、两端标签同一个日期 | 桶数 < 3 改成「N 周」一句话加一张数字表；起止相同的坐标轴只印一次日期 | `i3-weeks-*`（light / dark 各一） |
| 4 | 英文会话页工具栏在 1600px 下换行，排序控件掉到第二行 | 工具栏按三组（范围段 / 开关组 / 分组 + 排序）换行，不再单个控件掉行；两个最长的英文开关文案缩短 | `i4-toolbar-*` |
| 5 | 数据页英文 `Sessions (main / subagent / empty)` 标签换行，把值也挤成两行 | 标签改 `Sessions main/sub/empty`，值列不换行 | `i5-health-*` |
| 6 | daemon 写的中文解释在英文页上读起来像漏翻 | 英文页优先读 A9 给的 `*_en`；没有 `*_en` 的才照原样印中文并打一个 `zh` 小角标 | `i6-hint-*`、`i6-caliber-*`、`i6-hint-en-light-no-en-field.png` |

第 6 条中途改了做法。A9 交付之前，Go 侧没有英文写法，前端能做的只有「说明这是 daemon 原文」；A9 落地后每条解释都带上了英文字段（`hint.mechanism_en`、`category_rule_en`、`coverage_gaps[].mechanism_en`、`scope.note_en`、`parallelism`/`environment`/`subagents`/`reread` 的 `note_en`、`usage.note_en`/`definition_en`、`/sources` 的 `note_en`），所以现在是**两条路**：

| daemon 给了什么 | 英文页怎么印 | 中文页怎么印 |
| --- | --- | --- |
| 中文 + `*_en` | 印 `*_en`，没有角标 | 印中文 |
| 只有中文（A9 之前写下的记录） | 照原样印中文，末尾一个 `zh` 角标 | 印中文，没有角标 |

判据是文本本身有没有汉字，不是某个开关：`daemonProse(zh, en)` 挑出要印的那一句，`daemonCopyFlag(text)` 只在这一句里含汉字且当前是英文页时才出角标。所以 A 线补齐剩下的英文写法时，角标自己就不再出现，前端不用再改一次。**误读提醒**：角标不是「这里翻译错了」，是「这条解释是 daemon 写的证据原文，daemon 目前只给了中文」——页面从不改写 daemon 陈述的事实。

两条路都在真实数据上跑过：

- 现在的库里所有解释都有 `*_en`，英文页角标 **0** 个，英文页上 daemon 解释相关的汉字文本节点从 14 个降到 **0** 个（总览 7→0、项目页 6→0、数据页 1→0；项目页还剩 2 个汉字节点，是真实会话标题，本来就该按原文显示）。
- 用 CDP 在响应阶段把 `/api/v1/overview` 里 44 个 `*_en` 字段全部删掉再交给页面（daemon 与数据库都没动），角标立刻回来 7 个（机制 6、口径 note 1），正好是本轮记的那个分布，中文照原样印。这一张是 `i6-hint-en-light-no-en-field.png`。

第 2 条改的比 dogfood 里写的多一点。原记录说是 `.fl-spark-area`（线下填充）铺满了 34px；实测下来面积只占一小块，那块灰方块主要是 `.fl-spark-gap`——「这段时间没有记录到值」的满高灰带。两个点的序列里它占了 84% 的宽度，就成了整行唯一看得见的东西，而两个点之间因为不相邻，折线是两段各自孤立的 `moveto`，什么也画不出来。所以四件事一起改：面积不画、中位基线不画、灰带收成贴底的窄条并提高对比度，样本点真正画出来（零长度圆头描边，非等比缩放下也是正圆）。「没有记录到值」这个事实一条都没丢，只是不再压过样本本身。

改完在 36 个状态上重新量的结论：

- 六项必须为零的仍然全为零——`selects` 0、`imgs` 0、`dateInputs` 0、`decorativeGlyphs` 0、`consoleErrors` 0、`non2xxAPI` 0（`cache304` 4 次，仍是条件请求命中缓存）。
- 空图标槽 0，解析不出来的图标名 0，越出白名单的图标 0；实际画出 36 个（B7 是 34 个，多的两个就是这次补的）。
- 横向溢出的状态 0 个。
- 打上 `data-sparse` 的 sparkline 464 个，其中画了面积的 **0** 个。
- 英文页的 `zh` 角标 0 个（所有解释都有 `*_en`）；把 `*_en` 从响应里删掉后回到 7 个，见上。
- 会话页工具栏在 1280 / 1440 / 1600 / 1920 四个宽度下，中英文的行数完全一致：1600 和 1920 排一行，1280 和 1440 排两行且是在组的边界上断开的。1600px 下英文工具栏从 218px 高降到 181px，正好少了一行。
- 页内更新仍然不重建：会话页换排序，`#flatline-screen`、`.session-toolbar`、`.fl-scope-row` 和三个新加的分组容器前后都是同一批对象，整个动作窗口 `opacity` 恒为 1，`data-entering` 全程没出现过 `true`。

## 目录

- [current/](./current/) — 36 张截图（B7 拍的，第 1–6 条修之前的样子）。
- [b8-fixes/](./b8-fixes/) — B8 六处修复的前后对比截图。
- [data/manifest.json](./data/manifest.json) — 每张图的路由、语言、主题、daemon 数据摘要。
- [data/quality-checks.json](./data/quality-checks.json) — 六项归零的逐状态证据，以及页内更新不重建的四处采样。
- [data/component-inventory.json](./data/component-inventory.json) — 11 个组件的页面清单与观测方式。
- [data/icon-inventory.json](./data/icon-inventory.json) — 白名单、B4 三条新增、实际画出的图标、解析不出来的名字。
- [data/number-consistency.json](./data/number-consistency.json) — 分子分母、缺失措辞、与接口载荷的逐项对照。
- [data/kpi-row-geometry.json](./data/kpi-row-geometry.json) — 四个宽度下 KPI 行的列数与行分布。
- [data/scope-key-proof.json](./data/scope-key-proof.json) — 口径徽标跟随 `scope.key` 的端到端证据。
- [data/routes.json](./data/routes.json) — 本轮九个路由与它们的具体地址（详情页用的是真实 id）。

## 复跑

需要一个 `ready` 的 daemon 和一个开着 CDP 端口的无头 Chrome。脚本本身是一次性的，逻辑都写在上面的「截图方法」和各节的口径说明里；数据文件里每一项都带 `rule` 字段说明它是怎么判的。要重跑，按这三条重建即可：预置 `localStorage` 两个键、每次经 `about:blank` 强制新文档、六项判定分别按 `quality-checks.json` 里 `rule` 写的口径统计。
