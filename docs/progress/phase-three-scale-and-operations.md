# 改进阶段三实施记录：规模与运维能力

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

> 对应设计：[改进阶段三实施计划](../development/phase-three-scale-and-operations-plan.md)
>
> 记录日期：2026-09-05
>
> 范围：阶段三规模与运维改进；主题插件和已有运行产物不纳入本阶段。

## 1. 实施结论

阶段三的代码改进已完成，Go 全量测试、race、vet、模块完整性、性能门禁、阶段验收、SBOM 和许可证审查均通过。阶段三的核心目标已经落地：

- 内容、公开卡片、媒体和 taxonomy 列表使用轻量投影、批量富集和固定上限。
- 公开缓存 miss 使用有界 render flight 合并并发渲染，失败结果不会写入缓存。
- 后台任务记录最近开始/完成时间和耗时，并提供按状态、类型、租约和失败年龄聚合的快照。
- 备份支持本地/独立远程存储边界、可选 AES-GCM 分块加密、校验和验证以及隔离恢复入口。
- 管理端任务页显示脱敏后的运维摘要，不显示绝对路径、凭据、密钥或任务 payload。
- 新增 10,000 条长正文固定 fixture 和阶段三规模性能门禁。

本记录不把主题插件回归、已有 `.playwright-results/` 运行目录或 `themes/cel-panel/` 主题文件纳入交付范围。

10,000 篇文章的真实 HTTP 并发、冷缓存错误边界和瓶颈定位见[阶段三并发实测与瓶颈报告](./phase-three-concurrency-load-report.md)。

## 2. 主要实现

### 2.1 数据库和投影

- 新增 `db/migrations/00016_phase3_scale_operations.sql`。
- `jobs` 增加最近开始、完成和耗时字段；`storage_migrations` 增加媒体迁移游标、批大小和租约字段；`backups` 增加对象键、验证时间和脱敏错误摘要。
- publishing 将管理列表、公开列表、相关文章和 taxonomy 页面切换到列表/卡片投影；详情、编辑和归档等确需正文的路径仍保留完整读取。
- organization 增加按 content ID 和已发布快照的批量分类/标签读取。
- media 增加有界分页、变体摘要上限和公开媒体批量读取。

### 2.2 缓存和后台任务

- presentation 增加最大 32 个并发渲染 flight；同一 epoch、主题和页面 key 的并发 miss 只选一个 leader 渲染。
- leader 脱离客户端取消并受固定超时约束；错误、panic 和超时不会写入 PageCache。
- 页面缓存清理从每次 miss 的完整扫描改为节流/后台触发，并保留 epoch 失效语义。
- TaskQueue 在 claim、complete、fail 和 recover 时记录时间/耗时，`TaskSnapshot` 返回有限聚合，不读取或展示任务 payload。

### 2.3 存储、备份和运维

- media 存储迁移改为 `(media_id, variant_key)` 游标批处理，保留进度、租约、失败信息和校验后切换语义。
- 新增独立 `BackupStore` 边界；本地存储与媒体存储的远程适配互不共享生命周期前缀。
- 新增 AES-GCM-256 分块加密格式，支持受限密钥文件；密钥原文不写入数据库、日志、manifest 或管理页面。
- `backup create/verify/drill/restore` 支持加密归档和远程对象读取，校验失败或恢复失败不会替换当前站点数据。
- 配置增加 `[operations.backup]` 的 adapter、prefix、加密密钥文件和 S3 连接参数，默认仍为本地未加密备份。
- CLI、后台任务页和运维摘要输出目标、加密、验证和恢复演练时间等安全元数据。

## 3. 性能证据

阶段三规模测试使用 10,000 条已发布文章，每条正文为 5,824 bytes，包含分类和标签；列表页固定取 50 条。`internal/perf/phase3_scale_test.go` 验证公开列表和后台列表不携带 `body_markdown`，卡片批量读取保留顺序，堆内存增量不超过 32 MiB，三个查询均在 2 秒预算内完成。

`make perf-gate` 的一次完整结果：

| 指标 | 结果 |
| --- | ---: |
| fixture 内容数 | 10,000 |
| 单条正文 | 5,824 bytes |
| 公开列表（50 条） | 21.055 ms |
| 后台列表（50 条） | 239.379 ms |
| 公开卡片（50 条） | 4.923 ms |
| 列表查询堆内存增量 | -82,944 bytes |
| ADR-0031 | 通过 |

阶段验收脚本的性能重跑也通过；其日志中的公开列表、后台列表和卡片耗时分别为 11.607 ms、288.961 ms 和 11.694 ms，仍在固定门禁内。

## 4. 验收结果

| 检查 | 结果 | 说明 |
| --- | --- | --- |
| `go test -tags 'fts5 sqlite_omit_load_extension' ./... -count=1` | 通过 | 全量 Go 测试通过 |
| `make test-race` | 通过 | 包含阶段三规模、备份加密、缓存合并和任务快照测试 |
| `make vet` | 通过 | 固定 SQLite build tags |
| `go mod verify` | 通过 | all modules verified |
| `make perf-gate` | 通过 | ADR-0031 与阶段三规模门禁通过 |
| `make stage3-acceptance` | 通过 | 测试、race、vet、模块校验和性能门禁通过 |
| `make sbom` | 通过 | 报告写入 `dist/` |
| `make license-audit` | 通过 | 报告写入 `dist/LICENSE-REVIEW.txt` |
| `git diff --check` | 通过 | 无空白错误 |

## 5. 未执行项与剩余风险

1. 阶段验收中的 Docker Compose 健康检查因当前环境无 `/var/run/docker.sock` 权限而未执行；脚本其他验收项仍通过。需要在具备 Docker 权限的环境重新验证 `/livez`、`/readyz` 和首页检查。
2. `BROWSER_STRICT=1 make browser` 未进入浏览器用例：`npx @playwright/test --version` 在当前环境持续解析依赖，等待约 5 分钟后终止。需要在预装 Playwright/浏览器且站点已启动的环境重新执行三浏览器回归。
3. 本轮没有连接真实 S3 或兼容服务；远程存储的配置、边界和本地单元测试已覆盖，但真实网络超时、权限和远程 5xx 仍需在具备测试桶的环境执行。
4. 当前修改尚未创建 Git 提交。后续应按阶段三计划中的模块顺序分批提交，并明确排除 `themes/cel-panel/`、`internal/presentation/cel_panel_theme_test.go` 和 `.playwright-results/`。

## 6. 配置与回滚提示

- 不配置 `[operations.backup]` 时保持本地备份默认行为。
- 启用加密前应准备权限为仅 Owner 可读的 32-byte 原始或 64 位十六进制密钥文件；密钥丢失时不能恢复加密备份。
- 新迁移版本为 16；应用启动会自动迁移，回滚代码时应先按项目既有备份/恢复流程保留数据快照。
- 阶段三没有改变公开 permalink、单站点/单 Owner 模型、主题插件能力边界或默认主题回归范围。
