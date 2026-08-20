# DEVELOPMENT · Flatline 开发指南

> 本文件规定 Flatline 的构建、测试、质量门禁与目录约定。
> P1 骨架已落地；以下门禁与目录约定从现在起适用于实现代码。

## 1. 技术栈（目标）

| 层 | 选型 | 约束 |
| --- | --- | --- |
| 语言 | Go（稳定版，≥ 1.22） | 单二进制交付 |
| 存储 | SQLite，**纯 Go 驱动**（如 `modernc.org/sqlite`） | 禁止 CGO 依赖，保证交叉编译 |
| 运行形态 | 单 daemon，唯一数据属主 | 无多进程写同一 DB |
| API | 本地 HTTP，**仅绑定 loopback**（127.0.0.1） | 禁止 0.0.0.0 绑定，禁止外网依赖 |
| 前端 | SPA，构建产物经 `go:embed` 内嵌 | 构建产物不入库（`web/dist/` 已 gitignore） |
| 迁移 | `migrations/` 目录，编号 SQL 文件 | 只增不改（append-only），变更需新文件 |

## 2. 目录约定

```
cmd/            # daemon 与 CLI 入口（main 包）
internal/       # 全部业务代码（不可被外部 import）
migrations/     # SQLite schema 迁移（NNN_描述.sql，append-only）
web/            # SPA 源码（构建产物 web/dist/ 不入库）
scripts/        # 构建/检查/回测脚本
testdata/       # 合成 fixture（禁止真实用户数据）
docs/           # 设计文档、roadmap、ADR
```

- 业务代码一律放 `internal/`，`cmd/` 只做装配；
- 新增顶层目录需 ADR 或 roadmap 依据。

## 3. Go 质量门禁

每次提交/PR 必须通过：

```bash
gofmt -l .            # 输出必须为空
go vet ./...           # 必须通过
go test ./...          # 必须全绿
```

- 测试与源码同包（`_test.go`）；表驱动测试优先；
- 涉及 detector/状态机的测试必须基于 `testdata/` 中的合成 fixture 回放，fixture 变更需说明；
- 派生层（detector、状态机）必须携带 detector/schema 版本，支持全量重算（ADR-10）；
- 禁止在测试中访问网络；禁止测试写真实资产目录。

## 4. 前端质量门禁

`web/` 下（P5 起生效）：

```bash
npm run lint     # 必须通过
npm run test     # 必须全绿
npm run build    # 必须成功，产物经 go:embed 内嵌
```

- 组件必须实现 UI 指南 §9 组件清单的规格（含观测等级角标、分子分母展示）；
- 文案规则（UI 指南 §3.3）：只写对齐不写因果、比例带分子分母、unknown 有明确文案；
- 颜色语义遵循 UI 指南 §8（颜色必须与文字/形状共同表达）。

## 5. 测试分层

| 层 | 范围 | 工具/位置 | 要求 |
| --- | --- | --- | --- |
| 单元 | 纯函数：阈值判定、基线计算、形状归类、引用提取 | `*_test.go` | 每个 detector 判定规则 100% 覆盖边界（如机会数 = N-1 / N / N+1） |
| 集成 | 模块协作：adapter → event store → tracker → 状态机 | `internal/**/integration_test.go` | 基于 `testdata/` fixture 全链路回放 |
| 回测 | 状态机在 ≥3 个月合成历史上回放（真实历史回测为发布验收，见 roadmap P7） | `scripts/backtest` | 判定准确率人工核验，结果可复现 |
| 前端单元 | 组件渲染、状态徽章、sparkline 标记点 | `web/` 前端测试工具（P1 通过 ADR 冻结） | 覆盖 canonical 观测等级（含 `unknown`）与"未记录"态 |
| 端到端 | daemon 启动 → loopback API → SPA 黄金路径 | `scripts/e2e`（P5 后） | 6 步黄金路径 + 清理路径可走通 |

原则：

- 小任务先测试（AGENTS.md §4）：先写失败测试，再实现；
- 测试失败先怀疑实现，不修改断言迁就错误行为（除非断言本身错，需说明）；
- fixture 必须合成且自描述（文件名/头部注释说明场景与预期判定）。

## 6. 迁移规范

- 文件命名：`NNN_短描述.sql`（三位编号递增，如 `001_initial.sql`）；
- 只增不改：已合入的迁移文件永不修改，schema 变更一律新文件；
- 每个迁移附：前向 SQL + 回滚说明（或显式声明不可回滚及理由）；
- 派生表（opportunities、participations、vital_states 等）必须可整体重算——阈值调整后全量回放状态历史必须是廉价操作（ADR-10）；
- schema 变更需同步更新系统设计 §7 的对象模型表。

## 7. 构建与运行（目标形态）

```bash
# 构建单二进制（内嵌 SPA）
go build -o bin/flatline ./cmd/flatline

# 运行 daemon（loopback）
./bin/flatline daemon   # 监听 127.0.0.1:<port>

# CLI
./bin/flatline status   # 资产状态摘要
```

- 数据文件（`*.db`）位于用户本地数据目录，不入库、不上传；
- 首次运行执行历史回测，生命体征墙即刻点亮（验收标准 3）。

## 8. 隐私边界（开发红线）

- 不引入任何上传/遥测/第三方网络请求的依赖；
- 不引入账户/登录/云同步相关代码（MVP 明确不做）；
- `testdata/` 与 fixture 禁止包含真实用户 Session 数据；
- 本地 API 仅 loopback 绑定，代码审查时确认无 0.0.0.0/外网监听。

## 9. 交付检查（与 CONTRIBUTING 审查清单一致）

提交前自查：

- [ ] `gofmt` / `go vet` / `go test` 全绿（前端：lint/test/build）；
- [ ] 无伪造数据、无因果结论、无不透明分数；
- [ ] 资产写入/删除路径有显式确认与可回滚记录；
- [ ] 迁移 append-only，附回滚说明；
- [ ] 文档同步（设计文档/roadmap/ADR）；
- [ ] 无新增网络依赖、无遥测、无 MVP 范围外功能。