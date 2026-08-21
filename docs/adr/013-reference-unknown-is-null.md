# ADR-13: 引用体检未知结果保留为 NULL

- 状态：accepted
- 日期：2026-08-20
- 决策者：Flatline maintainers

## 背景

系统设计 v0.4 §3.1 要求 unknown 不得伪装成失败或零。旧的 `reference_check_items.exists` 约束为 `NOT NULL CHECK (exists IN (0,1))`，无法记录检查器未提供结果的引用；如果跳过该行，用户又无法看到“引用被提取但未体检”的事实。

## 决策

将 `reference_check_items.exists` 改为可空：`0` 表示检查器明确观察到不存在，`1` 表示明确存在，`NULL` 表示引用已提取但结果未记录。overall status 使用 `unknown`/`partial` 保留汇总语义，detector 只对明确的 `0` 触发 broken。

迁移采用新表复制、旧表替换的 SQLite 兼容方式，不删除已有行；派生状态仍可从引用检查事实重算。

## 备选方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 丢弃 unknown item | 不改 schema | 证据不可下钻，违反 unknown 显式化 | 拒绝 |
| 用 `0` 代表 unknown | 无迁移 | 产生伪失败和错误告警 | 拒绝 |
| `exists` nullable + overall status | 语义完整，可回放 | 需要一次结构迁移 | 采用 |

## 后果

引用体检结果能够完整展示 checked/failed/unknown；旧的 0/1 数据保持兼容。所有读 API 和 UI 必须把 NULL 显示为“未记录/不可观测”，不能渲染为 false 或绿色。
