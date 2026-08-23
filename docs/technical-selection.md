# 技术选型

## 结论

推荐以 Go 1.26.x、SQLite、服务端 HTML 和少量浏览器交互构建单进程应用。生产运行时不需要 Node.js、Java、Redis、独立搜索服务或独立任务队列。

Go 1.27 已于 2026-08-19 发布，但距离本设计确认只有四天。首个实现仍使用已确认且处于支持期的 Go 1.26.x；在 1.27 完成至少一轮依赖兼容、RSS、延迟和回归测试后再升级，不长期锁死 1.26。[Go 发行历史](https://go.dev/doc/devel/release)

## 推荐栈

| 层次 | 推荐 | 选择原因 |
|---|---|---|
| 语言 | Go 1.26.x | 单程序、低常驻资源、标准库完整，可用 `GOMEMLIMIT` 配合容器预算 |
| HTTP | `net/http` + `go-chi/chi/v5` | 完全兼容标准 Handler，路由可按模块挂载，核心很小且无外部依赖 |
| 数据库 | SQLite WAL | 单站点写入并发低，无独立数据库进程，备份与迁移简单 |
| SQLite 驱动 | `mattn/go-sqlite3` | 直接使用 SQLite C 实现，开启 FTS5 并静态编入发行物；CGO 只存在于构建环境 |
| SQL | 手写 SQL + `sqlc` | 生成类型安全 Go 代码，无 ORM 反射和运行时模型魔法 |
| 迁移 | `pressly/goose/v3` 嵌入式 SQL 迁移 | 支持 SQLite 与 `embed.FS`；生产只执行向前迁移 |
| Markdown | `yuin/goldmark` v1 稳定线 | CommonMark、GFM、脚注和 CJK 扩展，可扩展 AST；不使用仍处 beta 的 v2 |
| HTML 清洗 | `microcosm-cc/bluemonday` | 对 Markdown 渲染结果执行允许列表清洗；文章与评论采用不同策略 |
| 主题模板 | Go `html/template` | 运行时解析、上下文自动转义、无需主题构建工具 |
| 管理后台 | 服务端模板 + vendored HTMX 2.x | 服务器返回 HTML 片段，不维护完整 SPA 状态或通用写 API |
| 编辑器 | Milkdown 独立交互组件 | Markdown 原生所见即所得；只在编辑页加载，并保留源码模式 |
| 浏览器构建 | TypeScript + Vite | 仅开发与 CI 使用，产物哈希后嵌入 Go 二进制；生产无 Node.js |
| 样式 | 原生 CSS、Custom Properties、级联层 | 默认主题体积可控，主题作者无需 Tailwind 或构建步骤 |
| 密码 | `golang.org/x/crypto/argon2` | 使用 Argon2id，参数在 1 GiB 基准机上校准并随哈希保存 |
| TOTP | `pquerna/otp` | 实现 RFC 6238、二维码绑定和验证；恢复码由核心独立处理 |
| 本地存储 | `os`/`io` 原子文件操作 | 默认媒体和备份写入持久卷，不额外引入服务 |
| 对象存储 | AWS SDK for Go v2 S3 模块 | 只随 S3 官方插件初始化，支持自定义端点与 S3 兼容服务 |
| 图片 | Go 标准 JPEG/PNG 编码器 + `golang.org/x/image/draw` | 无外部图片进程；Catmull–Rom 高质量缩放，限制像素数并串行生成变体，优先稳定内存而非格式数量 |
| 日志 | 标准库 `log/slog` | 结构化日志且无额外框架，敏感字段集中脱敏 |
| 配置 | TOML 文件 + 环境变量覆盖 | 人类可读；秘密只从环境变量或文件引用读取，不使用全局动态配置框架 |
| TLS/代理 | Docker Compose 可选 Caddy | 自动 HTTPS、压缩和反向代理；已有代理的用户可不启用该 profile |
| 浏览器测试 | Playwright | 覆盖 Chromium、Firefox、WebKit，并可断言 ARIA 可访问树 |
| 性能测试 | Go benchmark + `vegeta` + Lighthouse CI | 分别覆盖热点函数、HTTP 吞吐和浏览器性能预算 |

相关一手资料：[chi](https://github.com/go-chi/chi)、[sqlc SQLite](https://docs.sqlc.dev/en/stable/tutorials/getting-started-sqlite.html)、[go-sqlite3](https://github.com/mattn/go-sqlite3)、[goose](https://github.com/pressly/goose)、[goldmark](https://github.com/yuin/goldmark)、[bluemonday](https://github.com/microcosm-cc/bluemonday)、[HTMX](https://htmx.org/docs/)、[Milkdown](https://milkdown.dev/docs/guide/why-milkdown)、[Caddy 自动 HTTPS](https://caddyserver.com/docs/automatic-https)。

## SQLite 运行约束

每个进程只打开一个写连接和一个小型只读连接池。启动时必须设置并验证：

- `PRAGMA journal_mode=WAL`
- `PRAGMA foreign_keys=ON`
- `PRAGMA busy_timeout`
- `PRAGMA synchronous=NORMAL`
- 有界 `cache_size` 和定期 checkpoint

所有写入均使用短事务。图片处理、网络请求、Markdown 渲染和 Webhook 发送不得占用数据库写事务。备份使用 SQLite 在线备份能力或等价一致性快照，绝不直接复制活跃数据库文件及忽略 WAL。

`go-sqlite3` 发行构建至少启用 FTS5，并禁用运行时加载任意 SQLite 扩展。Linux amd64/arm64、macOS arm64/amd64 和 Windows amd64 由 CI 分别原生构建；Docker 是首要发行物。

## 管理后台策略

普通列表、表单、筛选、审核和设置页由服务端完整渲染。HTMX 只负责局部替换、确认和渐进增强，且从本地产物加载，不依赖 CDN。Milkdown、媒体选择器、图片裁剪和拖拽导航是独立 TypeScript 组件；组件之间通过 DOM 事件与表单值交互，不建立全局客户端 store。

编辑器必须维护两个独立概念：Markdown 正文与编辑器视图状态。保存时只提交 Markdown；Milkdown 无法无损表达的自定义内容指令退回源码块编辑，不擅自改写。

## 图片策略

纯 Go 图像处理会按像素面积分配内存，因此先读取头部并拒绝超限尺寸，默认最大解码像素和单文件大小可配置。原始文件逐字节保留且不重新编码；变体队列并发固定为 1，使用高质量缩放生成少量宽度档位，JPEG 默认质量为 92，PNG 只做无损压缩。WebP 编码库必须通过恶意输入、峰值 RSS、画质和跨平台测试后才能进入依赖锁；失败时保留 JPEG/PNG 响应式变体，不因此引入常驻 libvips 服务。

## 明确不选

- Next.js/Payload、WordPress 或 Ghost 作为运行内核：常驻资源和扩展边界不符合已确认目标。
- PostgreSQL 默认部署：对单站点是额外进程、备份面和配置成本。
- Redis：缓存、会话和任务规模可由进程内有界缓存与 SQLite 持久队列承担。
- Elasticsearch/Meilisearch：SQLite FTS5 足以覆盖既定一万内容规模。
- 完整 SPA：增加重复 API、客户端状态和插件 UI 耦合。
- Go 原生动态插件、Lua 或任意脚本：破坏跨平台、安全与资源可预测性。
- 微服务和消息队列：没有独立伸缩或组织边界证据。

## 依赖准入

新增运行时依赖需要同时满足：明确解决已确认需求；许可证可与 Apache-2.0 发行兼容；仍在维护；不会新增常驻进程；具备可替换封装；通过 1 GiB 环境的内存和故障测试。锁定确切版本并生成 SBOM，不在生产构建中使用浮动 `latest`。
