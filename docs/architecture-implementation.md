# 实现说明

总体边界见[架构](architecture.md)。本页用于查找当前代码和排查运行问题。

## 代码目录

| 路径 | 职责 |
|---|---|
| `cmd/blog` | HTTP 服务与运维 CLI |
| `internal/app` | 组装、路由、插件初始化和生命周期循环 |
| `internal/platform` | 配置、SQLite、日志、公共 ID、HTTP/网络与密钥工具 |
| `internal/identity` | 唯一站主、密码、TOTP、恢复码、会话与安全设置 |
| `internal/publishing` | 内容、快照、修订、发布、排期、回收站与批量操作 |
| `internal/organization` | 分类、标签、导航、重定向与公开 taxonomy 投影 |
| `internal/media` | 上传、变体、引用、本地/S3 存储与迁移 |
| `internal/presentation` | Markdown、公开视图、缓存、主题安装和切换 |
| `internal/discovery` | FTS5、中文 gram、RSS、Sitemap、SEO 与 llms.txt |
| `internal/operations` | 数据锁、持久任务、备份恢复、升级、状态与审计 |
| `internal/extensions` | 插件 Host、注册、设置、事件、启停与移除 |
| `internal/comments`、`analytics`、`contentapi`、`webhooks` | 可选插件 |
| `internal/notifications` | SMTP、通知 outbox 与 Newsletter |
| `internal/importer`、`archive` | 离线导入与内容归档 |
| `web/admin`、`themes/default` | 内嵌后台与默认主题资源 |
| `themes/example`、`cel-panel`、`luminous-editorial` | 可打包的主题源码 |
| `db/migrations` | 嵌入式 Goose 迁移 |
| `tests/browser`、`scripts` | 浏览器回归、性能、供应链和发行检查 |

## 启动与配置

`app.New` 获取数据锁，打开数据库并自动迁移，加载或创建认证密钥，再组装身份、发布、媒体、主题、备份和可选能力。服务启动后，生命周期循环按有限批次执行搜索同步、taxonomy 重建、定时发布、插件任务、清理和备份。

数据目录默认包含 `db`、`media`、`cache`、`backups`、`secrets`、`themes`、`plugins`。运行时 `plugins` 数据目录与源码中的模块目录不是一回事。

配置来自默认值、TOML 和环境变量；应用不会自动读取 `.env`。Compose 只映射其 `environment` 中列出的变量。数据库保存的站点公开基址可覆盖启动时的基址，见[配置说明](usage.md)。

停止时结束生命周期循环、关闭 HTTP 服务和数据库、释放数据锁。外部任务使用短租约；进程退出后由过期租约恢复执行。

## SQLite 与迁移

`internal/platform/database` 维护一个写连接和默认两个读连接，启动验证 WAL、外键、busy timeout、同步模式和 FTS5。所有构建使用 `fts5 sqlite_omit_load_extension` 标签。

迁移文件当前编号为 `00001`–`00022`，最后两项记录插件与主题移除状态。实例实际版本用 `blog status` 或 `blog migrate` 查询。新增迁移追加编号，不能修改已应用文件；生产没有自动向下迁移流程。

列表查询避免携带正文，taxonomy 和封面批量富集。内容 API 游标使用 tuple 范围查询。搜索由 FTS5 和中文 gram 索引组成；正文变化标记 dirty，后台以有限批次重建。

## 发布与公开读取

发布服务在短事务中更新内容状态、创建不可变修订、保存引用、路径和审计，增加 `render_epoch`，登记 `ContentPublished.v1` 与派发任务。网络与图片处理在事务外。定时发布从数据库读取到期内容，使用条件更新防止重复发布。

公开路由解析路径后读取已发布视图，渲染和清洗 Markdown，再交给主题。草稿、编辑快照、预览和后台不会进入公共页面缓存。RSS、Sitemap、搜索和内容 API 同样只读取发布投影。

页面缓存键包含渲染版本。并发 miss 由有界 RenderFlight 合并；错误、panic 和超时不写缓存。旧缓存由有界清理回收，主题资源使用内容指纹 URL。

## 身份与 HTTP

登录和初始化为管理公共路由；其他管理页需要会话。写请求校验 CSRF、Origin、方法和版本锁，删除、批量或安全操作另有确认要求。不能把这些要求理解为所有 `/admin/*` 都需登录，或所有 POST 都需额外确认。

会话令牌只存哈希，TOTP 密钥加密，恢复码一次性使用。管理员变更密码或第二因素会撤销旧会话。评论和 Newsletter 使用限流、幂等/短期指纹；可信转发头只接受来自配置 CIDR 的直接对端。

公开 CSP 允许同源资源，为最终输出的内联 script/style 内容生成精确 SHA-256 授权；不开放 `unsafe-inline` 或内联 style 属性。模板使用 `html/template` 自动转义，文章与评论采用不同清洗策略。

## 任务与插件

`jobs` 保存状态、可见时间、载荷版本、幂等键、租约、尝试次数和有限错误摘要。claim、完成和失败状态使用短事务，处理器在事务外工作。后台提供失败任务人工重试，复用原行并记录审计。

业务事务登记 `event_outbox` 与 `core:event_dispatch`，提交后调用启用订阅者。目前对外事件为 `ContentPublished.v1` 和 `CommentApproved.v1`。Webhook 从事件创建稳定投递身份、HMAC 签名与重试任务。

插件注册先在临时注册表完成，成功后合并。停用或移除后路由、事件和任务消费均受宿主检查，配置和数据保留。Host 是窄接口，但可信 Go 插件本身不在进程沙箱中；不能声称它能够隔离恶意代码。详细方法见[插件开发](development/plugins.md)。

## 主题与媒体

主题安装校验包大小、文件数、解压总量、路径、符号链接、特殊文件、清单、模板和代表性样例。数据库激活记录为权威，`active.json` 为可重建标记。激活失败保持当前主题，内嵌默认主题可回退。

媒体上传限制字节和像素，检测 MIME，保留原始字节并生成受限变体。引用包括 Markdown 正文和封面，删除前验证引用。主题设置中的媒体值只向模板暴露公开 URL、尺寸、替代文本和 srcset。

## 外部网络限制

| 适配器 | 当前行为与限制 |
|---|---|
| Webhook / 外部 Newsletter | 使用 `netguard`，限制超时、重定向和私网连接；由持久任务重试 |
| SMTP | 初始连接经 `netguard`，默认连接超时 15 秒；后续 SMTP 交互未设置完整连接 deadline |
| S3 | 自定义 SigV4，HTTP 客户端超时 30 秒；未统一接入 `netguard`，默认客户端可能跟随重定向 |

这些差异来自 `internal/notifications/mail.go` 和 `internal/media/storage.go`，启用 S3/SMTP 前需核对端点和网络策略。默认站点不启用这些外部服务。任务失败不回滚已经成功的内容发布；上传、存储迁移等直接调用则向调用者返回错误。

## 备份与恢复

备份使用 SQLite 一致性快照，文件清单记录大小与 SHA-256，包含数据库、媒体、主题、插件数据与认证密钥，不包含缓存或其他备份。支持本地/远端目标和可选加密密钥文件。

恢复验证归档后解包到隔离目标；`--replace` 交换数据目录并保留旧数据。恢复演练只使用隔离目录，不替换现行实例。操作步骤见[使用手册](usage.md)。

## 验证范围

仓库提供 Go 测试、race、vet、性能基准、浏览器回归、SBOM 和许可证检查脚本。`make release` 默认生成 Linux amd64/arm64 OCI 归档，推送镜像需显式 `PUSH=1`。Dockerfile 最终镜像为 distroless nonroot，仍使用 CGO SQLite；不能按纯 Go 静态二进制部署。

[ADR-0031](adr/0031-performance-budgets-are-release-gates.md) 中的延迟、吞吐和资源指标是验收目标，不是任意 VPS 的保证。目标容器的 RSS/heap/GC/WAL、真实 Caddy/TLS、封面媒体容量与完整浏览器夹具需要在发布环境检查。历史结果见[历史索引](history.md)。
