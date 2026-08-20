# CONTRIBUTING · Flatline 贡献指南

## 1. 总则

- 先读 [AGENTS.md](AGENTS.md)（执行规范）与 [DEVELOPMENT.md](DEVELOPMENT.md)（开发流程），再动手。
- 任何改动必须能追溯到设计文档（`docs/flatline-system-design-v0_4.md`、`docs/flatline-ui-design-guidelines-v2_0.md`）或 roadmap 阶段目标；设计文档未承诺的功能不实现。
- 有疑问先提问，不猜测；架构级分歧走 ADR 流程（见 `docs/adr/README.md`）。

## 2. main 分支保护思路

- `main` 是**随时可构建、测试全绿**的受保护分支：
  - 不直接 push 到 `main`（本地与远端均如此）；
  - 所有变更经 PR + 审查合入；
  - 合入前 CI 必须全绿（见 DEVELOPMENT.md 质量门禁）；
  - `main` 上的提交历史保持线性、可追溯（squash 或 rebase 合入，二选一后全项目统一）。
- 破坏性操作（force push、删除分支、改写历史）仅限维护者，且需说明理由。

## 3. 分支命名

| 类型 | 格式 | 示例 |
| --- | --- | --- |
| 功能 | `feat/<roadmap阶段>-<短描述>` | `feat/p2-source-adapters` |
| 修复 | `fix/<短描述>` | `fix/silent-detector-threshold` |
| 文档 | `docs/<短描述>` | `docs/adr-11-loopback-only` |
| 重构 | `refactor/<短描述>` | `refactor/event-store-locator` |
| 实验/草稿 | `wip/<短描述>` | `wip/sparkline-markers` |

- 短描述用小写连字符短语，不超过 5 个词；
- 分支从最新 `main` 切出，合入前 rebase 到 `main`。

## 4. 提交规范（Conventional Commits）

格式：`<type>(<scope>): <subject>`

| type | 用途 |
| --- | --- |
| `feat` | 新功能（对应 roadmap 交付物） |
| `fix` | 缺陷修复 |
| `test` | 仅测试 |
| `docs` | 仅文档 |
| `refactor` | 不改变外部行为的重构 |
| `chore` | 构建/工具/杂项 |
| `perf` | 性能改进 |

规则：

- subject 用祈使句、小写开头、不加句号，≤ 72 字符；
- scope 建议用模块名（`adapters`、`state-machine`、`api`、`web`、`migrations`、`docs`）；
- 破坏性变更用 `!` 或 footer `BREAKING CHANGE:` 标注；
- **提交信息禁止因果句式**（"X 导致 Y"），只陈述事实与对齐；
- 不提交生成物（`bin/`、`dist/`、`*.db`）、不提交真实用户数据。

示例：

```
feat(state-machine): implement silent detector with configurable threshold

- 连续 N 个机会零参与判定（默认 N=8，基线要求：历史参与 ≥5 次且参与率 ≥30%）
- 阈值集中配置，判定依据快照写入 vital_states
- 测试：testdata/silent-scenario fixture 回放

Refs: docs/roadmap.md#p4
```

## 5. 代码质量门禁

每次 PR 必须通过（详见 DEVELOPMENT.md）：

- Go：`gofmt` 无 diff、`go vet` 通过、`go test ./...` 全绿；
- 前端：lint、test、build 全绿；
- 迁移：`migrations/` 变更必须可前向应用且附回滚说明（或声明不可回滚及理由）。

## 6. 文档与 ADR

- 行为变更必须同步更新对应设计文档或 roadmap；文档与代码不一致时，以"先改文档、再改代码"为原则（设计先行）。
- 架构级决策（新增模块、改变数据模型、推翻既有 ADR）必须先落 ADR：`docs/adr/NNN-<标题>.md`，流程见 `docs/adr/README.md`。
- 设计文档版本号递增（v0.4 → v0.5），变更记录写在文档头部。

## 7. 隐私边界（贡献者红线）

- 不引入任何网络上传、遥测、第三方服务依赖；
- 不引入需要登录/账户的功能（MVP 明确不做）；
- fixture 与测试数据一律合成，禁止包含真实 Session 内容；
- 本地 API 只允许 loopback 绑定。

## 8. 审查清单（Reviewer 逐项确认）

- [ ] 变更可追溯到设计文档/roadmap 阶段目标；
- [ ] 无伪造数据、无因果结论、无不透明分数（AGENTS.md §2 逐条核对）；
- [ ] 涉及资产写入/删除的路径有显式确认与可回滚记录；
- [ ] 测试覆盖新增行为；fixture 明确标注；
- [ ] 比例/判定均带分子分母与观测等级；
- [ ] 未引入 MVP 范围外的功能（账户/云同步/AI/桌面壳/群体参考/插件/改善轮次）；
- [ ] 无新增网络依赖、无遥测；
- [ ] 提交信息符合 Conventional Commits，无因果句式；
- [ ] 文档（设计文档/roadmap/ADR）已同步；
- [ ] CI 全绿（fmt/vet/test/lint/build）。

## 9. 交付物要求

每个 PR 描述必须包含：变更摘要、测试（命令与结果）、风险、未完成项（与 AGENTS.md §5 一致）。