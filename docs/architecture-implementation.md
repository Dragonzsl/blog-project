# 实现级架构与技术细节

本文档把当前代码组织成一份可以用于开发、排障和评审的实现说明。它描述的是仓库当前实现，而不是把所有设计计划都当作已经完成的功能；计划、ADR 和历史验收记录仍以[文档索引](./README.md)中的原文为准。

## 1. 系统定位和部署边界

项目是一个 Go 1.26 模块化单体。一次部署只承载一个 `Site` 和一个 `Owner`，公开端、管理后台、生命周期任务和 CLI 共享领域服务、数据库和配置。

```text
访客 / Feed Reader / API Client
              │
       HTTP/HTTPS + Caddy
              │
        Go blog application
       ┌──────┼────────┐
       │      │        │
     Public  Admin    CLI/Jobs
       └──────┼────────┘
              │
       SQLite WAL + data volume
       ├── db/blog.sqlite
       ├── media / cache / backups
       ├── secrets / themes / plugins
       └── durable jobs + event outbox
              │
       optional adapters
       S3 / SMTP / Newsletter / Webhook
```

默认 Compose 由三个服务组成：

- `data-init`：以 root 身份初始化 `/data/site` 目录和非 root 应用的所有权，只运行一次。
- `app`：以 UID/GID `65532` 运行，根文件系统只读，监听容器内 `:8080`，限制为 `0.85 CPU`、`256 MiB`、128 个 PID。
- `caddy`：终止 HTTP/HTTPS、压缩并反向代理到 `app:8080`，限制为 `0.15 CPU`、`64 MiB`、64 个 PID。

生产不需要 Node.js、Redis、独立 PostgreSQL、独立搜索服务或消息队列。Node/Playwright 只用于开发和浏览器验收，不进入最终应用镜像。

## 2. 代码分层和依赖方向

```text
HTTP / CLI adapter
        │
application service
        │
repository port / SQLite adapter
        │
after-commit event + durable job

theme renderer ── immutable public view model
official plugin ── narrow extensions.Host capability
```

当前主要目录职责：

| 目录 | 当前实现职责 |
|---|---|
| `cmd/blog` | 启动、迁移、健康检查、认证恢复、备份/恢复、升级、主题、归档、存储迁移、导入、审计、状态和版本命令 |
| `internal/app` | 应用组装、路由注册、插件初始化、生命周期循环和关闭流程 |
| `internal/platform` | 配置、SQLite、日志、ID、slug、分页、客户端 IP、公开写入保护、网络边界和密钥 |
| `internal/identity` | 唯一站主、密码、TOTP、恢复码、会话、CSRF、Origin 和授权 |
| `internal/publishing` | 文章/页面、编辑快照、不可变版本、发布、定时发布、撤回、回收站和批量操作 |
| `internal/organization` | 分类、标签、导航、永久链接、重定向和公开 taxonomy 投影 |
| `internal/media` | 上传、MIME/像素检查、原图/变体、引用关系、本地/S3 存储和迁移 |
| `internal/presentation` | Markdown 安全渲染、公开 view model、主题、预览和页面缓存 |
| `internal/discovery` | FTS5、中文 gram 辅助搜索、RSS、Sitemap、robots、llms.txt 和 SEO |
| `internal/operations` | 数据锁、任务、备份、恢复、升级、健康检查、审计和运维摘要 |
| `internal/extensions` | 插件 Registry、Host、路由槽、设置 schema、事件和持久任务 |
| `internal/comments` | 本地/外部评论插件及管理审核 |
| `internal/analytics` | 隐私优先的本地聚合统计 |
| `internal/contentapi` | 可选只读 Content API v1 |
| `internal/notifications` | SMTP、Newsletter、通知 outbox 和异步发送 |
| `internal/webhooks` | 签名 Webhook、投递记录和持久重试 |
| `internal/importer` / `internal/archive` | WordPress/Ghost/Markdown 离线导入和内容归档往返 |
| `web/admin` | 通过 `embed.FS` 嵌入的后台模板、CSS、JS |
| `themes/default` | 嵌入式默认主题和安全 fallback |
| `db/migrations` | Goose 顺序迁移，当前工作树包含 `00020` |
| `tests/browser` / `scripts` | 浏览器夹具、性能门禁、阶段验收、SBOM、许可证和发行脚本 |

HTTP handler 不直接写 SQL；写入必须进入应用服务或模块 repository。模块不查询其他模块的私有表，跨模块读取通过服务接口、专用查询或公开投影完成。

## 3. 启动、初始化和生命周期

### 3.1 启动顺序

`blog serve` 的主要启动步骤如下：

1. 读取默认配置、TOML 和环境变量。
2. 解析相对路径，以配置文件目录为基准定位数据目录、数据库和密钥文件。
3. 校验监听地址、代理 CIDR、数据库参数、媒体限制、外部 endpoint 和插件配置。
4. 获取数据目录锁，确保同一数据目录只有一个应用或运维命令写入。
5. 打开 SQLite，设置 WAL、外键、busy timeout、同步模式、cache size 和 FTS5，并自动执行向前迁移。
6. 加载或创建认证密钥，初始化身份、发布、组织、发现、媒体、主题和备份服务。
7. 注册核心任务和可选插件；插件先在临时注册表完成路由、菜单、事件和任务注册，全部成功后才合并。
8. 建立 Chi 路由和安全中间件，启动 HTTP server 与生命周期循环。
9. 生命周期循环执行有限批次的搜索同步、taxonomy 重建、定时发布、插件任务、回收站清理、公开写入记录清理和定时备份。

插件初始化失败会记录错误并保持核心站点启动，不能因为可选插件故障阻断文章发布和公开读取。

### 3.2 关闭和恢复

收到 SIGINT/SIGTERM 后，应用先停止生命周期循环，再按 `server.shutdown_timeout` 等待 HTTP 请求退出，最后关闭数据库和数据锁。任务使用短租约；进程在任务处理期间退出时，过期租约会让任务重新可执行。

## 4. HTTP 路由和安全边界

### 4.1 中间件

根路由按顺序使用：

1. `RequestID`：为请求生成可关联的 ID。
2. `CleanPath`：规范化路径。
3. Recoverer：捕获 panic，返回统一错误页并记录脱敏日志。
4. Access log：记录必要的请求摘要，不记录密码、token、邮箱明文和完整访客隐私数据。

管理路由位于 `/admin` 下：

- 登录、初始化和认证静态资源属于管理公共路由。
- 其他管理路由经过 `RequireSession`。
- 写请求经过 CSRF、Origin、会话和请求方法校验。
- 评论、Newsletter 重发、发布、恢复、批量和删除等高影响操作还需要显式确认。
- 安全响应头由管理 handler 和 Caddy 共同提供，模板使用 `html/template` 自动转义。

公开写入（评论、Newsletter）额外使用客户端 IP 解析、限流、请求幂等键和短期重复指纹。只有显式配置且命中 `trusted_proxy_cidrs` 的代理才可以提供可信转发头；否则使用直接 TCP 对端地址。

### 4.2 路由分组

公开核心路由包括首页、文章/页面、归档、分类、标签、搜索、RSS、Sitemap、robots、llms.txt、媒体和预览。管理路由按身份、内容、组织、媒体、主题、插件、任务和运维分组。

可选路由只在对应插件启用后有效：

- 评论：`/posts/{slug}/comments` 和插件管理路径。
- Newsletter：`/newsletter/subscribe`、确认/退订路径和订阅者管理路径。
- Content API：`/api/v1/site`、`/api/v1/posts`、`/api/v1/pages` 及单项接口。
- Webhook：没有公开写入入口，只有后台状态和提交后任务。

## 5. 权威数据和标识策略

### 5.1 数据权威

数据库中的站点、文章、页面、分类、标签、导航、媒体和修订是权威数据。以下内容都必须可以从权威数据重建：

- 页面缓存和渲染快照。
- FTS5 文本索引和中文 gram 索引。
- taxonomy 投影。
- 媒体变体。
- 统计聚合的派生部分。
- 主题缓存和 `active.json`。

公开读取永远使用已发布修订；编辑快照和草稿不能通过公开路由、RSS、Sitemap、搜索或 Content API 泄露。

### 5.2 ID、slug 和时间

- SQLite 内部自增 ID 只用于内部连接和排序，不进入公开 URL。
- 需要跨归档、API 或实例引用的对象拥有 16 字节稳定 `public_id`。
- 文章/页面公开身份是稳定 slug。已发布 slug 变化必须保留旧路径并写入重定向。
- 时间以 UTC Unix 毫秒持久化；展示时使用站点时区。
- `content_revisions` 不可变；恢复会创建新版本，不修改旧版本。

### 5.3 迁移

迁移通过嵌入式 Goose 文件顺序执行，应用启动时自动升级。禁止编辑或重排已应用迁移；新增 schema 必须增加下一个编号，并覆盖空库、升级库和重启/失败恢复。

当前迁移按能力分组：

- `00001` 平台基础，`00002` 身份，`00003` 内容/发布，`00004` 组织/媒体。
- `00005` 内容生命周期，`00006` 搜索/发现/重定向，`00007` SEO，`00008` 运维。
- `00009` 扩展基础，`00010` 阶段三导入/Webhook，`00011`–`00013` 阶段一安全/队列/Newsletter hash。
- `00014` 封面，`00015` 主题契约，`00016` 阶段三规模/运维，`00017` taxonomy 投影，`00018` 后台内容索引。
- `00019` 阶段四产品能力，`00020` Feed 摘要策略。

实际版本以 `blog migrate` 或 `blog status` 为准；历史 progress 文档中的迁移版本只代表当时的验收快照。

## 6. 内容发布事务

一次手动发布的大致流程：

```text
HTTP form
  │ CSRF / Origin / session / lock_version
  ▼
Publishing service
  │ validate slug/status/snapshot
  │ short SQLite write transaction
  ├─ update content state
  ├─ insert immutable content_revision
  ├─ update media references
  ├─ reserve old permalink / create redirect
  ├─ increment render_epoch
  ├─ write audit entry
  ├─ insert event_outbox(ContentPublished.v1)
  └─ enqueue core:event_dispatch
  ▼ commit
  │
  └─ lifecycle task: search / taxonomy / warmup / webhook
```

事务内不执行 Markdown 重渲染、图片处理、网络请求、邮件和 Webhook。提交失败时业务记录、审计、事件和任务一起回滚；提交后外部副作用失败只进入任务重试，不回滚已发布内容。

定时发布不是内存 timer。生命周期循环从数据库读取到期内容，使用带条件的更新确保同一内容只发布一次；应用重启后由同一数据库任务继续处理。

## 7. SQLite、查询和投影

数据库层维护一个 writer 和有界 reader pool。WAL 允许公开读与短写并行，但 SQLite 仍受单写者和磁盘 I/O 限制。所有写事务应短小，不能在事务内做远程 I/O、图片处理或大批量 Markdown 运算。

阶段三加入了面向列表和公开卡片的轻量投影：

- 列表查询不携带 `body_markdown`。
- 分类/标签通过批量查询和 `public_taxonomy_members` 投影富集。
- 后台内容索引使用 `(kind, trashed_at, updated_at, id)` 等证据驱动索引。
- 媒体列表按页返回，变体摘要有上限，公开卡片按公共媒体 ID 批量解析封面。
- 10,000 篇数据的 Content API cursor 使用 tuple 范围条件，不使用 `limit * page` 扫描大页码。

搜索由 SQLite FTS5 和中文字符 gram 辅助索引组成。dirty 文档同步在事务外准备文本，在短 writer 批次中替换索引。搜索同步、taxonomy rebuild 和清理都按 tick 设置固定批次，避免长期占用 writer。

## 8. 页面渲染和缓存

公开页面渲染链路：

1. 解析 slug、重定向和查询语义。
2. 读取站点/主题版本和 `render_epoch`，组成公开缓存 key。
3. PageCache 命中时直接返回快照、ETag 和缓存头。
4. miss 时读取已发布 view model，渲染 Markdown 并做文章清洗。
5. 主题模板使用安全公开 view model 生成完整 HTML。
6. 成功响应才写入内存/磁盘缓存；错误、预览、搜索、后台和带身份差异的响应不进入公共页面缓存。

同一个缓存 key 的并发 miss 由有界 `RenderFlight` 合并，leader 与客户端请求取消解耦，并受独立超时约束。失败、panic 和超时不能写入 PageCache。缓存失效依赖单调递增的 `render_epoch`，旧文件由后台/阈值清理，不在每个请求中全量扫描。

默认主题资源通过内容指纹 URL 提供，页面不默认请求第三方字体、图标、分析脚本或 CDN。当前配置的默认主题 CSS/JS 预算和 UI 回归要求见 [ADR-0031](./adr/0031-performance-budgets-are-release-gates.md) 与 [UI 计划](./ui-refactor-plan.md)。

## 9. 后台任务、事件和插件

### 9.1 任务模型

`jobs` 保存 kind、payload version、幂等键、状态、可见时间、尝试次数、短租约、最近错误和耗时。任务状态为 `pending`、`running`、`succeeded`、`failed`；最多有限次指数退避，最终失败保留在后台可见列表。

任务处理规则：

- claim、complete、fail、recover 都是短事务。
- payload 有大小上限；大文件只保存媒体/备份对象键。
- 处理器必须幂等、可重入，不能依赖内存状态保证唯一执行。
- 插件停用时不接收新的事件、不执行外发，但保留任务和数据；重新启用后可继续处理。
- 人工重试复用原任务行并写审计，不能通过新建任务绕过幂等键。

### 9.2 Outbox

业务事务先写 `event_outbox`，再创建 `core:event_dispatch` 任务；只有提交后 dispatcher 才调用启用的订阅者。当前参考事件契约要求：

- `ContentPublished.v1`：手动发布和定时发布。
- `CommentApproved.v1`：评论审核通过。

事件 payload 只携带公开对象 ID、kind、slug 等小字段，不携带正文、凭据和邮箱。Webhook 会从事件派生稳定 delivery identity，使用 HMAC-SHA256 和任务重试。

### 9.3 插件 Host

插件通过 `extensions.Host` 注册：

- 命名空间化的公开/管理路由。
- 标准 UI slots 和菜单。
- 版本化 settings schema 与迁移。
- 版本化事件订阅。
- 有界 payload 的持久任务和 retry hook。

插件拿不到裸 `*sql.DB`、通用文件系统、进程执行器或任意模板集合。设置 schema 总 JSON 上限为 64 KiB，敏感值读取脱敏，插件路由必须经过 enabled 检查。

## 10. 主题系统

主题是 ZIP 资源包，不是插件。安装器验证：

- `theme.json`、模板和资源是否存在且版本兼容。
- 文件数量、压缩包大小、解压总量、路径穿越、绝对路径、符号链接和特殊文件。
- 所有 Go `html/template` 是否可以解析。
- 空站、长文、中英文混排、大图、无封面、长标签、404 等代表性 fixture。
- ZIP checksum 和确定性目录 checksum。

数据库中的 `themes.active`、设置和激活历史是权威；`active.json` 只是可重建 marker。激活采用候选验证、原子目录切换和失败回退，启动 reconcile 会重新检查当前主题。内嵌默认主题永远作为安全 fallback。

主题只接收不可变公开视图模型：`SiteView`、`ContentView`、`CollectionView`、`NavigationView`、`MediaView` 和 `PageContext`。模板不能读取环境变量、数据库、私有配置或执行代码。

## 11. 媒体和对象存储

媒体服务先限制请求大小，再检测 MIME、读取图片头部和像素尺寸；原始对象保持字节保真，派生图使用固定宽度和 JPEG/PNG 策略。媒体引用包括正文 Markdown 和结构化封面，删除前会检查引用。

存储抽象只有 `Put`、`Open`、`Delete` 和名称等最小能力，当前适配器包括：

- `LocalStorage`：数据目录内原子临时文件写入和 rename。
- `S3Storage`：自定义 endpoint、path-style/virtual-host-style、SigV4、媒体迁移和备份对象前缀复用。

设计要求所有外部存储请求都具备 timeout、响应大小限制、重定向禁止和私网地址策略。Webhook/Newsletter 已使用 `internal/platform/netguard`；截至当前代码审计，S3 适配器仍使用普通 `http.Client`，SMTP 也只在初始连接阶段使用网络边界。因此启用 S3/SMTP 前应按当前安全审计结论谨慎验证，不应把它们描述为已完全闭合的生产安全边界。

## 12. 备份、恢复和升级

备份流程使用 SQLite 一致性快照，不直接复制活跃主库文件。归档包含：

- 数据库及迁移信息。
- 媒体、主题、插件和认证秘密。
- manifest、文件大小和 SHA-256 checksum。
- 应用版本、commit、迁移版本、目标和加密状态。

缓存和其他备份不进入备份归档。备份可使用本地 store 或独立 prefix 的远程 store，可选受限密钥文件加密。上传成功并完成验证后才把记录标记为 valid。

恢复先校验外层 checksum、manifest、路径、文件大小、条目哈希和迁移版本，再解包到隔离目标。`--replace` 只在完整校验成功后原子交换数据目录，并保留 rollback 目录。`backup drill` 不影响现行实例，只写隔离目录并更新恢复演练时间。

## 13. 外部网络和秘密边界

当前外部网络调用包括：

| 适配器 | 触发方式 | 失败语义 |
|---|---|---|
| Webhook | 提交后事件任务 | HMAC 签名、有限重试、永久策略错误进入 failed |
| 外部 Newsletter | 订阅/退订持久任务 | 超时、4xx/5xx 分类和有限重试 |
| SMTP | 通知 outbox 任务 | 不阻断内容发布，失败进入可见任务状态 |
| S3 媒体/备份 | 上传、读取、删除、迁移/备份 | 不在业务写事务中执行，失败由调用方报告或重试 |

外部 endpoint 不应允许凭据、无必要的 query/fragment 或不受控重定向。私网/loopback 访问必须有明确的安全理由和显式策略，不能靠默认网络可达性解决。

## 14. 资源与性能边界

设计容量基线约为 10,000 篇文章/页面、100,000 条评论、100,000 项媒体、每月 1,000,000 页面浏览，以及短时约 100 RPS 缓存命中。

当前有界措施包括：

- 数据库 writer=1、reader 默认=2，reader 上限 8。
- PageCache 和搜索快照有条目/字节上限。
- RenderFlight 最大并发 32。
- 生命周期任务每个 tick 只处理固定批次。
- 公开列表、媒体、taxonomy、搜索和归档不无界加载正文。
- 上传、主题包、ZIP 导入、任务 payload、响应和解压均有上限。
- 图片派生处理不允许无限并发。

ADR-0031 的目标门槛为：缓存命中 p95 ≤25 ms、首次渲染 p95 ≤150 ms、搜索 p95 ≤250 ms、常规后台 p95 ≤300 ms、100 RPS 错误率 <0.1%，且内存不持续增长。当前应用层测试覆盖主要查询和 10,000 篇 API 读取，但目标容器下的 RSS/heap/GC/WAL、真实 Caddy/TLS、真实封面媒体和部分浏览器证据仍需单独验收。

## 15. 构建、测试和发行

固定构建标签：

```text
fts5 sqlite_omit_load_extension
```

主要质量门：

```bash
make test
make test-race
make vet
go mod verify
make perf-gate
make stage3-acceptance
make browser
make sbom
make license-audit
```

Dockerfile 使用 Go 1.26.0 构建、静态裁剪 binary，最终镜像为 distroless nonroot。`scripts/release.sh` 默认构建 `linux/amd64,linux/arm64` OCI 归档，并生成 SBOM、许可证报告和 SHA-256 清单；是否真的完成多架构构建取决于发布机的 buildx 能力。

## 16. 当前实现与设计文档的差异

以下项目需要在后续维护中保持明确，不应混入“已实现”列表：

1. `docs/technical-selection.md` 推荐 Milkdown、HTMX、Vite、AWS SDK 和 sqlc；当前实现采用原生 textarea/Vanilla JS、服务端模板、自定义 S3 HTTP 适配和手写 SQL。这是技术选型漂移，不是当前核心流程不存在。
2. `docs/extensions.md` 列出若干事件示例，而 [事件参考](./reference/events.md) 和当前代码只把 `ContentPublished.v1`、`CommentApproved.v1` 作为明确契约；新增事件前应先冻结契约。
3. 历史阶段文档中的迁移版本、Compose 和浏览器结果只代表记录当时的环境；当前状态必须用命令和最新进度文档复核。
4. 默认主题核心与用户主题插件必须保持提交和回归边界分离；主题插件运行产物不应被当成默认主题的质量证据。

## 17. 维护时不可破坏的不变量

- 不把缓存、搜索索引、统计或媒体变体当作权威数据。
- 不用内部 SQLite ID 生成公开 URL。
- 不让公开路由读取草稿或编辑快照。
- 不在业务写事务中做网络、邮件、Webhook、图片处理或大段 Markdown 运算。
- 不绕过 `render_epoch`、版本锁、数据锁和任务幂等键。
- 不扩大代理可信 CIDR 到默认路由。
- 不把密码、token、密钥、邮箱明文或完整访客隐私数据写入日志、审计、任务错误或归档。
- 不通过添加无界连接、缓存、队列或 goroutine 规避资源预算。
