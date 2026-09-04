# AGENTS.md

## 适用范围

本文件适用于仓库根目录及其全部子目录。当前仓库没有更深层的 `AGENTS.md`；如果未来新增，应遵循“越靠近目标文件越具体”的规则。

这是一个面向自托管的单站点个人博客：Go 1.26 模块化单体、SQLite WAL、Markdown 正文、服务端渲染，以及默认关闭的可选扩展。公开页面、管理后台、任务执行器和 CLI 共用同一个应用程序；生产运行时不需要 Node.js、Redis、独立搜索服务或消息队列。

## 开始工作前

1. 先运行 `git status --short`，保留用户已有的修改、未跟踪文件和运行产物；不要用 `git reset --hard`、`git checkout --` 或清理命令覆盖它们。
2. 先读与任务直接相关的设计文档。通常从 [`CONTEXT.md`](./CONTEXT.md)、[`docs/architecture.md`](./docs/architecture.md)、[`docs/data-model.md`](./docs/data-model.md)、[`docs/extensions.md`](./docs/extensions.md) 和 [`docs/adr/README.md`](./docs/adr/README.md) 开始；主题、插件、导入、恢复或性能任务还要读对应的 `docs/development/`、`docs/security/`、`docs/progress/` 和 ADR。
3. 以当前代码和测试验证“已经实现的行为”；设计文档可能同时记录未来目标。发现文档与代码冲突时，不要默默扩大范围：说明冲突，必要时同步修正文档或新增 ADR。
4. 先找现有服务、查询、模板和测试，再决定是否需要新增抽象。优先做小范围、可回滚的改动。
5. 除非用户明确要求，不要自行 commit、push、发布镜像或修改远端资源。

## 不可破坏的产品与架构边界

- 一次部署只有一个 `Site` 和一个 `Owner`；不引入通用用户、角色、多租户、读者账户或会员体系。
- 文章和页面是固定的两种内容类型。数据库中的 Markdown 是正文权威来源；缓存、搜索索引、统计和媒体变体都必须可以从权威数据重建。
- 内容的公开身份是稳定的永久链接/slug，不是内部 SQLite 行号。已发布 slug 变更必须保留旧路径并创建重定向。
- `content_revisions` 不可变；编辑快照不是正式版本；公开读取不能读取编辑快照或草稿。
- 公开页面只使用已发布修订。会改变公开呈现的写操作要递增 `system_state.render_epoch`，让旧页面缓存失效。
- 一个业务写操作使用一个短 SQLite 写事务。事务内不做 Markdown 重计算、图片处理、网络请求、邮件或 Webhook 投递；外部副作用在提交后通过持久任务执行。
- 主题是受限的 Go `html/template` 模板和静态资源，不能执行服务端代码；插件是随程序编译的可信 Go 代码，不能上传、执行任意脚本或拿到裸 `*sql.DB`、通用文件系统和进程执行器。
- 插件默认关闭；停用插件保留配置和数据。插件路由必须经过宿主的启用检查，并使用自己的命名空间；事件和任务必须版本化且幂等。
- 站点默认不向第三方请求字体、分析脚本、图标或 CDN 资源。新增外部网络访问必须有明确的适配器、超时、失败语义和安全审查。
- 资源预算是设计约束，不是可无限放宽的建议：应用运行在约 0.85 CPU/256 MiB 容器预算内，Go 堆软上限默认为 192 MiB；缓存、连接池、队列和并发都应有界。

## 代码地图与依赖方向

| 路径 | 职责 |
| --- | --- |
| `cmd/blog/main.go` | `serve`、`healthcheck`、认证恢复、备份/恢复、主题、归档、存储迁移、导入、状态和审计 CLI |
| `internal/app` | 应用装配、路由注册、生命周期循环和可选插件初始化 |
| `internal/platform` | 配置、SQLite、日志、秘密、ID、slug、分页和 HTTP 公共件 |
| `internal/identity` | 唯一站主、密码、TOTP、恢复码、会话、CSRF 和授权 |
| `internal/publishing` | 文章/页面、编辑快照、不可变版本、发布、定时、撤回和回收站 |
| `internal/organization` | 分类、标签、导航、永久链接和重定向 |
| `internal/media` | 媒体、引用、变体、本地/S3 存储和迁移 |
| `internal/presentation` | Markdown 安全渲染、公开视图模型、页面缓存和主题加载/切换 |
| `internal/discovery` | 搜索、RSS、Sitemap、robots、SEO 和公开发现数据 |
| `internal/operations` | 数据锁、任务、备份、恢复、升级、健康检查和审计 |
| `internal/extensions` | 插件 Host、注册表、设置、事件和持久任务执行器 |
| `internal/comments`、`analytics`、`contentapi`、`webhooks`、`notifications`、`importer`、`archive` | 已实现的可选插件、通知、离线导入和内容归档能力 |
| `web/admin` | 服务端管理模板、CSS/JS，并通过 `embed.FS` 嵌入二进制 |
| `themes/default` | 内嵌故障回退默认主题；模板、CSS/JS 也是嵌入资源 |
| `themes/*`（如 `themes/example` 或本地主题包） | 主题包源码/示例，不是运行时插件目录 |
| `db/migrations` | 按序号排列、通过 `embed.FS` 嵌入的 Goose SQLite 迁移 |
| `tests/browser`、`tests/fixtures` | 三浏览器 Playwright 回归和导入夹具 |
| `scripts` | 性能门、阶段验收、发布、SBOM、许可证审查和浏览器回归 |

依赖方向应保持为：

```text
HTTP / CLI adapter -> application service -> repository port/adapter -> SQLite
                                   -> after-commit event / durable job
theme renderer     -> immutable public view model
official plugin    -> narrow extensions.Host capability
```

HTTP handler 不直接写 SQL；模块不要查询其他模块的私有表。跨模块协作通过服务接口、专用查询或版本化事件完成。不要新增跨项目的 `controllers/`、`services/`、`repositories/` 水平分层，也不要创建“万能 AppContext”或全局 service locator。

注意：设计文档中的 `plugins/` 是概念目录；当前代码没有顶层 `plugins/`，官方插件实现和注册入口在上述 `internal/*` 包及 `internal/app/app.go`。

## SQLite、迁移与数据规则

- 构建和测试始终启用 `fts5 sqlite_omit_load_extension` 两个 build tag。`go test ./...` 不是本项目的标准验证命令。
- `internal/platform/database` 维护一个单写连接和有界读连接池，并检查 WAL、外键和 FTS5。保持 `PRAGMA journal_mode=WAL`、`foreign_keys=ON`、busy timeout、同步模式和有界 cache 的约束。
- 新 schema 变化新增下一个顺序迁移，例如 `db/migrations/00011_short_name.sql`，包含 Goose 的 `-- +goose Up` 和可审查的 `-- +goose Down`；已经应用的旧迁移不要改写或重排。
- 迁移由 `database.Open` 自动执行；`blog migrate` 会打开数据库并输出迁移版本，不是生产回滚工具。新增迁移必须覆盖空库、已有库和失败重启场景。
- 时间持久化使用 UTC Unix 毫秒；公开或跨实例对象需要稳定 `public_id`。不要把内部自增 ID 写入公开 URL。
- 写事务要短并保持幂等。不要直接复制活跃 SQLite 主文件而忽略 `-wal`/`-shm`；备份使用项目现有的一致性快照流程。
- 每个新索引都要有真实查询依据。永久清理、任务重试、定时发布和缓存清理都应分批、有界，避免长事务和无限增长。

## 安全规则

- 管理后台和公开写接口保持现有会话、CSRF、Origin、限速和安全响应头策略；新增 POST/PUT/DELETE 端点必须覆盖鉴权、CSRF、重放/幂等和错误响应。
- Markdown 正文和评论使用各自的允许列表清洗；默认不允许原始 HTML 形成脚本注入通道。主题模板使用 `html/template` 自动转义，不增加通用 `safeHTML` 绕过。
- 不在日志、审计、测试输出、错误信息或提交中写入密码、会话令牌、TOTP、恢复码、API key、Webhook 密钥、SMTP 密码或完整访客隐私数据。
- 上传、ZIP、主题包和导入文件都要限制单文件/总大小、文件数、路径和解压行为；拒绝路径穿越、绝对路径、符号链接和特殊文件。媒体以检测到的 MIME 和解码结果为准，默认拒绝 SVG。
- 备份包含认证秘密，默认未加密且权限为 `0600`；不要把备份提交到 Git 或公开存储。恢复和危险删除前先校验归档、获取数据锁并保留可回滚副本。
- 网络适配器必须设置超时、拒绝不必要的重定向和私网穿透，并把远程失败转换为可重试的持久任务；网络不可用不能回滚已经成功的内容发布。

## 前端、主题和无障碍

- 管理后台和公开页面优先服务端完整渲染；JavaScript 只是渐进增强。不要引入完整 SPA、全局客户端 store 或新的前端框架，除非需求和 ADR 明确批准。
- 修改模板、CSS 或 JS 后保持无 JavaScript 可读/可操作的基本路径、键盘焦点、语义 landmark、ARIA 状态、可见焦点环、足够的触控区域和响应式无横向溢出。
- 公共端、后台、登录、初始化和预览共享主题偏好与一致的抽屉/键盘语义；桌面、平板、移动端及窄/矮视口都要考虑。
- 默认主题通过 `themes/default/embed.go` 嵌入；不要在模板中读取环境变量、文件、数据库实体或插件私有配置。主题只消费安全、不可变的公开视图模型。
- 安装/启用主题必须保留当前可用主题作为回退，并经过包限制、模板解析、API 兼容和真实样例渲染；失败不能让半成品主题接管站点。
- 保持 ADR-0029/ADR-0031 中的默认主题体积、第三方资源、LCP/INP/CLS 和公开页面响应预算；UI 改动至少跑相关 Go 渲染测试和浏览器回归。

## 常用命令

所有 Go 命令都从仓库根目录执行，并使用项目固定 tag：

| 命令 | 用途 |
| --- | --- |
| `make test` | 全部 Go 测试 |
| `make test-race` | race 检测 |
| `make vet` | `go vet` |
| `make build` | 构建 `bin/blog` |
| `make run` | 使用 `config.example.toml` 本地运行 |
| `make perf-gate` | ADR-0031 性能预算测试和 benchmark |
| `make stage3-acceptance` | 测试、race、vet、模块校验及（若 Compose 已启动）健康/首页检查 |
| `make browser` | 对默认 `BASE_URL=https://localhost` 跑 Chromium/Firefox/WebKit；需要站点和 Playwright |
| `BROWSER_STRICT=1 make browser` | Playwright 不可用时也失败，适合发布验收 |
| `make sbom` / `make license-audit` | 生成 `dist/` 下的供应链报告 |
| `make release` | 测试、vet、多架构 OCI、SBOM、许可证审查和校验和 |
| `docker compose config` | 校验 Compose 配置 |
| `docker compose up --build -d` | 构建并后台启动本地 app + Caddy |
| `docker compose down` | 停止本地服务；不会删除命名卷 |
| `go mod verify` | 校验依赖模块完整性 |

本机没有 Go 时，优先使用项目脚本的 Docker 回退；需要只跑全量测试时可执行：

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  sh -ec 'go test -tags "fts5 sqlite_omit_load_extension" ./...'
```

`make release` 默认只生成本地 OCI 归档；只有用户明确要求推送时才设置 `PUSH=1`。`dist/`、`bin/`、缓存、报告和运行数据是生成物，不要手工提交或用它们替代源码测试。

浏览器回归默认使用 `https://localhost` 并忽略本地 HTTPS 错误；若测试临时实例，设置 `BASE_URL`。`ARTICLE_SMOKE=1` 只在已准备好隔离文章夹具时启用，否则该用例会按设计跳过。

## CLI 与运维注意事项

常用子命令包括：`blog healthcheck`、`blog status`、`blog audit list`、`blog backup create|verify|drill|list`、`blog upgrade prepare`、`blog restore`、`blog theme install|list|activate|rollback`、`blog archive export|verify|import`、`blog storage migrate` 和 `blog import wordpress|ghost|markdown`。

本地启动通常是：

```bash
cp .env.example .env
docker compose up --build -d
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

`BLOG_SITE_ADDRESS` 是 canonical、Open Graph、RSS、Sitemap、robots 和 `llms.txt` 的公开基址，部署前必须改成访问者实际使用的地址。秘密优先通过环境变量、Docker secret 或权限受限文件注入。

默认 Compose 使用 `app` 和 `caddy`，应用数据位于命名卷中的 `/data/site`；普通 `docker compose down` 不会删除数据。执行 `blog restore --replace` 前必须先停止正式 app；恢复后先检查健康状态和公开结果，再处理输出的旧数据回滚目录。

## 按改动类型验证

- Go 业务/服务改动：先跑相关包的 `go test -tags 'fts5 sqlite_omit_load_extension' ./internal/<package>`，然后 `gofmt`、`make test`、`make vet`。
- 身份、权限、上传、主题包、导入、备份/恢复、迁移或外部网络改动：补充失败路径、重启/幂等/隔离测试，至少跑 `make test-race`；涉及发布或恢复时跑 `make stage3-acceptance`。
- 数据库改动：新增迁移和约束测试，验证空库与升级库；确认读写连接、WAL、备份和恢复仍可用。
- 公开页面、后台模板、CSS/JS 或主题改动：保持模板嵌入测试，跑 `make test`、`make browser`，必要时 `BROWSER_STRICT=1 make browser` 和 `make perf-gate`。
- 发布或依赖改动：跑 `go mod verify`、`make test-race`、`make vet`、`make perf-gate`、`make sbom` 和 `make license-audit`；新增依赖必须锁定版本、确认许可证、评估内存/故障影响，并提供可替换边界。

## 完成标准

提交前确认：

- 变更只覆盖用户请求和必要的测试/文档，没有顺手清理或重置用户文件。
- 所有改动均已 `gofmt`（Go 文件）、通过 `git diff --check`，没有秘密、临时数据库、备份或生成物进入提交。
- 相关测试和质量门已运行；无法运行时明确说明原因（例如本机缺少 Go、Docker、Playwright 或外部服务），不要声称“已通过”。
- 行为、配置、CLI、主题/插件契约或运维流程有变化时，README、对应设计文档或 ADR 已同步。
- 最终说明改了什么、验证了什么、剩余风险或未运行的检查。
