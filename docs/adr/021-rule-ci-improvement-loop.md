# ADR-21: 规则层 CI——从"监护仪"到"改进闭环"

- 状态：accepted
- 日期：2026-08-25
- 决策者：用户（产品方向指令）+ 主 agent 提案

## 背景

产品此前回答"发生了什么/哪里反复卡住"，但停在描述：用户看到签名与计数后没有下一步
（"我想优化但不知道怎么优化"）。外部调研（2026-08-25）给出三个依据：

1. **研究界**：AWM（arXiv:2409.07429）从 agent 自身历史归纳工作流再喂回，成功率 +24.6%/+51.1%；
   ExpeL（AAAI-24）证明不改权重、只积累自然语言经验即可持续提升。
2. **厂商**：Claude Code 官方文档要求用户**手工**完成"注意到重复 → 写规则 → 自己判断有没有用"，
   并自认规则"只是上下文不是强制"；auto memory 单 harness、不可审计、无验证。原始转写按
   `cleanupPeriodDays` 被厂商删除——本地事实层本身就有抢救价值。
3. **社区**：ccusage（18.2k stars）读同一批本地数据但只做费用报表；"规则质量"无人占位。

结论：产品定位从"agent 工作历史监护仪"升级为——**规则层（AGENTS.md / rules / skills / hooks）
的 CI**：覆盖率（覆盖缺口）、回归检测（摩擦签名+生命周期）已有，补上缺失的两拍——
**修复建议的生成**与**修复有效性的验证**。

## 决策

新增两块，全部遵守既有纪律（无 AI 层、写入显式确认、判定一句话可解释）：

1. **规则简报（rule brief，只读投影）**：`/api/v1/friction` 的 signature 分组行新增 `brief` 字段。
   内容全部来自既有事实：机制（提示字典）、规模（次数/会话/项目/首末时间）、3 条真实样例、
   落点建议（rule / hook / skill / environment / workflow，按机制类别确定性映射，附理由）、
   以及一段**可直接粘贴给用户自己 agent 的提示词**（中英各一份）。
   Flatline 不写规则、不调模型：写规则这一步交给用户最信任的 agent，Flatline 只负责证据。
2. **签名验证（signature watch，唯一新写入路径）**：新表 `signature_watches`（迁移 `022`）。
   用户确认"我已为这条签名写了规则"后创建一条 watch（`confirmed` 不为 true 一律拒绝，AGENTS.md §3）；
   状态机在读取时评估：
   - `verified`：创建后最近 `window_days` 天内该签名零发生，**且**同项目窗口内确有会话在跑
     （区分"修好了"与"没再跑"）；
   - `no_change`：窗口内仍发生；
   - `unobservable`：窗口内同项目没有会话，无法判断；
   - `watching`：创建未满一个窗口。
   判定规则一句话可解释，随响应携带 `criterion` / `criterion_en`。

### 落点建议的确定性映射（写进代码与文档）

| 机制类别 | 建议落点 | 理由（一句话） |
| --- | --- | --- |
| user_hook / permission | hook | 需要强制拦截，上下文形态的规则不保证被遵守 |
| environment | environment | 修 PATH / 装包，写规则不解决 |
| user_interrupt | workflow | 中断是用户自己的动作；简报改为工作流反思（中断前的模式） |
| timeout / tool_misuse / harness_rule | rule | 行为指引类，上下文规则适用 |
| test / build | hook | 提交/收尾前强制跑检查，比提醒可靠 |
| 机制未记录 | unrecorded | 机制未知，简报只带样例，请 agent 先判断 |

## 后果

- 正向：产品从"看历史"变成"改进循环的控制器"；验证拍是 LLM 单次会话给不了的纵向答案，
  是本产品独有的价值面。
- 代价：`signature_watches` 是事实层之外第一张"用户意图"表——它与 append-only 事件不同，
  是可删除的用户状态；删除同样显式（cancel 端点），不物理删除行而是置 `status='cancelled'`。
- 不做：内置 AI 起草（简报交给用户自己的 agent）；通知系统接入 watch 状态变化
  （先在摩擦页呈现，接入 /notifications 留待下轮）；逐消息 token 入库（第三拍，另立迁移）。
