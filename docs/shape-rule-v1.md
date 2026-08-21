# Flatline 任务形状规则 `shape/1`

这条规则回答一个问题：两个 Session 是否属于同一类机会？系统只使用调用方提供的任务标签，或 native history reader 从有界用户任务文本按固定规则产生的标签；不引入模型或跨用户比较。

Native history 的标签来源是可审计的：只有识别到有意义的用户任务文本时才产生标签，关键词映射是代码中的封闭规则，无法归类时使用 `recorded-task`，工作目录只作为 `workspace-<ascii-slug>` 标签。会话 ID、事件数、资产数量都不参与任务形状判定。

Native history 的机会集合仍更严格：只收录任务文本中的精确资产路径、明确的项目相对路径，或唯一资产 basename，以及 transcript 工具输入中已经解析出的明确资产调用。仅因为会话存在、工作目录相同或 transcript 有消息，不会生成 opportunity。

## 判定

1. 每个标签转为小写 ASCII 字符；非字母数字字符的连续区间折叠为一个 `-`，并去掉首尾 `-`。
2. 丢弃归一化后为空的标签，并对剩余标签去重。
3. 按字典序排序后，以 `shape/1:<tag>|<tag>` 作为 shape class。
4. 没有可用标签时不产生 shape class，也不产生机会记录；这表示未记录形状，不是零机会的替代值。

因此，标签顺序和重复次数不改变分类结果。例如 `SQL`、`sql`、`run sql` 的分类依据分别是 `sql`、`sql`、`run-sql`。

## 可追溯字段

每条 `opportunities` 记录携带 `shape_rule_version = shape/1`，`shape_class` 保存归一化后的确定性结果；基线查询同时返回该规则版本。规则变更时递增版本，并从 canonical 事实重新计算派生记录。
