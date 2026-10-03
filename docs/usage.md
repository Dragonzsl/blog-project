# 项目使用与运维手册

本文档说明当前仓库代码的实际使用方式，适用于本地开发、Docker Compose 自托管和发布前验收。它不替代安全策略或 ADR；涉及数据恢复、主题包、插件和外部网络时，应同时阅读对应的设计文档。

## 1. 产品边界

这是一个单站点、单站主的个人博客系统：

- 内容类型只有文章和页面，正文的权威格式是数据库中的 Markdown。
- 公开端、管理后台、后台任务和 CLI 由同一个 Go 程序承载。
- SQLite 使用 WAL；默认只有一个写连接和有界读连接池。
- 默认主题和所有官方插件都在程序内或嵌入资源中提供；可选能力默认关闭。
- 主题只能呈现公开视图，不能执行服务端代码；插件是随程序编译的可信 Go 代码。

以下能力不属于当前产品范围：多租户、多站点、角色/RBAC、读者账户、付费会员、自定义内容类型、页面搭建器、公开写 API、GraphQL、Redis、独立搜索服务和消息队列。

## 2. 快速启动

### 2.1 Docker Compose

要求：Docker Engine 或 Docker Desktop，以及 Compose v2。

```bash
cp .env.example .env
docker compose config
docker compose up --build -d
docker compose ps
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

默认拓扑如下：

| 入口 | 地址 | 说明 |
|---|---|---|
| Caddy HTTP | `http://localhost` | 公开入口，生产环境通常会重定向或由站点地址决定协议 |
| Caddy HTTPS | `https://localhost` | 默认公开入口；本地使用 Caddy 内部证书，命令行需要 `--insecure` |
| Go 应用 | Compose 内部 `app:8080` | 不直接发布到宿主机；健康检查访问 `http://127.0.0.1:8080/readyz` |
| 健康检查 | `/livez`、`/readyz` | 存活只检查进程；就绪检查数据库和迁移状态 |

`compose.yaml` 默认给应用设置 `0.85 CPU`、`256 MiB` 和 `GOMEMLIMIT=192MiB`，给 Caddy 设置 `0.15 CPU`、`64 MiB`。这些是资源边界，不应通过无界增加连接池、缓存或 goroutine 来绕开。

### 2.2 第一次初始化

启动后访问：

```text
https://localhost/admin/setup
```

按页面完成：

1. 设置站点名称、站主用户名和至少 12 个字符的密码。
2. 绑定兼容 TOTP 的验证器。
3. 离线保存页面显示的一次性恢复码。
4. 使用新的密码和 TOTP 登录 `/admin/login`。

恢复码只在生成时显示。若丢失认证信息，使用服务器管理员权限执行 `blog auth recover`，不要把新密码写在 shell 命令参数中。

### 2.3 本地 Go 启动

开发机已安装 Go 1.26 和 C 编译器时：

```bash
cp .env.example .env
make build
./bin/blog serve --config config.example.toml
```

默认监听 `:8080`。如果直接使用 HTTP 本地开发，应把公开基址设为 HTTP，避免浏览器因 Secure Cookie 无法保存会话：

```bash
BLOG_BASE_URL=http://localhost:8080 ./bin/blog serve --config config.example.toml
```

也可以在配置文件中修改 `[server].listen_address` 和 `[discovery].base_url`。

## 3. 配置说明

配置加载顺序为：内置默认值 → TOML 文件 → 环境变量覆盖 → 路径解析与校验。通过 `--config` 指定 TOML；未指定时读取 `BLOG_CONFIG_FILE`，否则使用内置默认值。

相对路径以配置文件所在目录为基准，数据目录内默认包含：

```text
<data_dir>/
├── db/blog.sqlite       SQLite 主库及 WAL/SHM 文件
├── media/               本地媒体对象
├── cache/pages/         可删除、可重建的公开页面缓存
├── backups/             本地备份归档
├── secrets/auth.key     认证加密秘密，权限 0600
├── themes/              已安装主题包
└── plugins/             插件相关数据或运行目录
```

### 3.1 常用配置项

| 配置 | 默认值 | 用途 |
|---|---:|---|
| `server.listen_address` | `:8080` | Go HTTP 监听地址 |
| `server.shutdown_timeout` | `10s` | 优雅停止等待时间 |
| `server.trusted_proxy_cidrs` | 空 | 允许读取转发客户端 IP 的代理网段 |
| `storage.data_dir` | `./data` | 数据目录 |
| `storage.adapter` | `local` | `local` 或 `s3` 媒体存储 |
| `database.path` | `db/blog.sqlite` | 相对于数据目录的数据库路径 |
| `database.read_connections` | `2` | 受限读连接池，允许范围 1–8 |
| `media.max_upload_bytes` | `12 MiB` | 单个上传大小 |
| `media.max_image_pixels` | `16,000,000` | 图片解码像素上限 |
| `media.variant_widths` | `640,1280` | 派生图片宽度 |
| `publishing.scheduler_interval` | `15s` | 定时发布与后台生命周期 tick |
| `publishing.editing_snapshot_interval` | `15s` | 编辑快照间隔 |
| `publishing.revision_limit` | `50` | 普通版本保留上限，发布检查点另行保护 |
| `publishing.trash_retention_days` | `30` | 回收站保留时间 |
| `discovery.base_url` | `http://localhost:8080` | canonical、Feed、Sitemap、JSON-LD 的基址 |
| `operations.backup_interval` | `24h` | 定时备份间隔 |
| `operations.backup_daily_retention` | `7` | 日备份保留数量 |
| `operations.backup_weekly_retention` | `4` | 周备份保留数量 |
| `security.session_lifetime` | `12h` | 登录会话有效期 |
| `extensions.theme_package_max_bytes` | `32 MiB` | 主题 ZIP 上限 |
| `extensions.theme_max_files` | `256` | 主题文件数上限 |
| `extensions.theme_max_unpacked_bytes` | `64 MiB` | 主题解压总量上限 |

### 3.2 环境变量覆盖

Compose 示例优先使用 `.env` 注入部署值：

```dotenv
BLOG_SITE_ADDRESS=https://blog.example.com
BLOG_AUTH_SECRET=<至少 32 字节的随机值>
BLOG_TRUSTED_PROXY_CIDRS=172.30.0.0/24
BLOG_MEMORY_LIMIT=192MiB
BLOG_LOG_LEVEL=info
BLOG_LOG_FORMAT=json
```

Compose 会把 `BLOG_SITE_ADDRESS` 转换为应用使用的 `BLOG_BASE_URL`。直接运行二进制时使用 `BLOG_BASE_URL`。

秘密优先使用环境变量、Docker secret 或权限为 0600 的文件。不要把以下值提交到 Git、日志或审计：密码、TOTP、恢复码、会话、S3 密钥、SMTP 密码、Webhook 秘密、Newsletter token 和备份解密密钥。

## 4. 公开端使用

默认主题提供以下公开路径：

| 路径 | 功能 |
|---|---|
| `/` | 首页 |
| `/articles` | 文章列表和分页 |
| `/posts/{slug}` | 文章详情 |
| `/{slug}` | 页面详情 |
| `/archive`、`/archive/{year}/{month}` | 年月归档 |
| `/categories`、`/categories/{slug}` | 分类索引与详情 |
| `/tags`、`/tags/{slug}` | 标签索引与详情 |
| `/search` | 全文搜索、类型/分类/标签筛选 |
| `/rss.xml` | RSS |
| `/sitemap.xml` | Sitemap |
| `/robots.txt` | Robots 规则 |
| `/llms.txt` | 公开内容摘要入口 |
| `/media/{public_id}/{variant}/{filename}` | 媒体原图或派生图 |

公开页面只读取已发布修订，不读取草稿或编辑快照。文章更改公开内容后会增加 `render_epoch`，旧页面快照自然失效。

文章详情支持阅读进度、目录、上一篇/下一篇、相关文章、复制链接、系统分享、打印和可选评论/Newsletter 插槽。关闭 JavaScript 时，核心导航、搜索和阅读路径仍保持服务端渲染。

## 5. 管理后台使用

所有 `/admin/*` 受站主会话、CSRF 和 Origin 检查保护。危险操作还需要服务端确认字段，即使绕过浏览器脚本也不能直接执行。

### 5.1 内容

- `/admin/articles`：文章内容库，可按状态、关键词、分类、标签和时间筛选。
- `/admin/pages`：页面内容库。
- `/admin/articles/new`、`/admin/pages/new`：新建内容。
- 编辑页：保存标题、摘要、Markdown、slug、分类、标签、封面、SEO 字段。
- 编辑快照：定时保存临时编辑状态；快照不是正式版本，不会公开。
- 预览：使用当前编辑状态渲染，但不进入公共缓存。
- 发布：创建不可变发布版本并登记 `ContentPublished.v1` 事件。
- 定时发布：写入 `scheduled_at`，由数据库任务在应用重启后继续处理。
- 撤回：保留内容身份和历史版本，停止公开读取。
- 回收站：支持恢复和批量恢复；超过保留期后由有界清理任务处理。
- 版本：查看、比较和恢复历史版本。恢复会创建新的 `restore` 版本，不修改旧版本。

批量发布、撤回、回收站和恢复都使用锁版本检查；遇到并发编辑冲突时应刷新页面并重新确认目标版本。

### 5.2 组织、媒体和主题

- `/admin/organization`：分类、标签和导航。
- `/admin/redirects`：查看或添加永久链接重定向；系统会阻止循环并保留旧公开路径。
- `/admin/media`：上传媒体、填写替代文字、复制 Markdown、查看引用数量和删除保护。
- `/admin/themes`：安装、预览、设置、激活和回退主题。
- `/admin/plugins`：查看官方插件状态、能力和初始化结果，并启停插件。

媒体删除前会扫描正文和结构化封面引用。主题激活失败时保持当前主题或内嵌默认主题，不让不完整包接管站点。

### 5.3 站点设置和运维

- `/admin/settings`：站点名称、公开基址、主要语言、时区、SEO 默认值、Feed 摘要策略、社交链接和默认社交图片。
- `/admin/settings` 的密码、TOTP、恢复码和会话撤销操作均要求当前认证信息或显式挑战。
- `/admin/operations/tasks`：查看 pending/running/failed 任务、租约和脱敏错误，并对失败任务执行人工重试。
- `/admin/analytics`：启用隐私统计后查看 7/30/90 天聚合数据；公开端不加载统计脚本。
- `/admin/plugins/comments.local/comments`：本地评论审核和批量审核。
- `/admin/plugins/newsletter.local/subscribers` 或对应外部插件路径：Newsletter 订阅状态和确认邮件重发。

## 6. 内容发布建议流程

推荐的稳定工作流：

1. 先创建草稿，确定标题、摘要和 slug。
2. 选择一个分类，可添加多个标签；如果需要封面，先上传媒体并填写替代文字。
3. 使用预览检查 Markdown、目录、图片、链接和 SEO 元数据。
4. 正式保存后再发布或安排发布时间。
5. 发布后检查文章永久链接、RSS、Sitemap 和搜索结果。
6. 修改已发布文章时，保存会形成新版本；撤回、恢复和 slug 变化都会保留旧路径重定向。

Markdown 正文是唯一正文来源。主题只消费清洗后的 HTML 和公开视图，不应在主题模板中拼接不受信任 HTML。

## 7. 可选能力

所有可选能力默认关闭，核心站点不依赖外部服务。

| 能力 | 配置 | 公开/后台效果 |
|---|---|---|
| 本地评论 | `comments.enabled=true`、`provider=local` | 文章评论、审核、批量审核、审核通过事件 |
| 外部评论 | `comments.enabled=true`、`provider=external` | 使用受控外部评论入口，不启用本地评论存储 |
| 隐私统计 | `analytics.enabled=true` | 内存聚合后写入日/月统计表，后台查看，默认不追踪 |
| 本地 Newsletter | `newsletter.enabled=true`、`provider=local` | 加密邮箱、确认 token、退订 token 和后台任务 |
| 外部 Newsletter | `provider=external`、配置 endpoint/token | 通过有界 HTTP 适配器异步同步 |
| 只读 Content API | `content_api.enabled=true` | `/api/v1/site`、`/api/v1/posts`、`/api/v1/pages`，见 [API 文档](./api/content-api-v1.md) |
| 签名 Webhook | `webhooks.enabled=true` | 发布/评论审核事件异步 HMAC 投递和重试 |
| S3 媒体 | `storage.adapter=s3` | 上传、读取、删除和本地/S3 迁移 |
| SMTP | `mail.enabled=true` | 评论/Newsletter 等通知通过 SMTP 任务发送 |

外部 endpoint、token 和密钥只应在受保护配置中提供。启用插件后，先在管理后台确认其路由、任务和失败状态，再投入生产流量。

## 8. CLI 参考

以下命令都支持 `--config FILE`；容器部署时通常用 `docker compose exec -T app /blog ...`。

后台 `/admin/plugins` 支持停用、删除和重新添加。删除会自动停用并移出已安装列表，保留配置、业务数据和待处理任务；删除前需勾选确认。重新添加后保持停用，需手动启用。已删除状态在重启及显式插件启用配置下都会保留。

### 8.1 生命周期、健康和认证

```bash
blog serve --config config.toml
blog migrate --config config.toml
blog healthcheck --url http://127.0.0.1:8080/readyz
blog status --config config.toml
blog version

# 从 stdin 恢复站主认证；不会把密码放进命令行参数
printf '%s\n' 'new-password' | blog auth recover --username owner --password-file -
```

`status` 重点查看 `migration_version`、`pending_jobs`、`running_jobs`、`failed_jobs`、`valid_backups`、最近备份和最近恢复演练时间。

### 8.2 备份、恢复和升级

```bash
blog backup create --config config.toml
blog backup list --config config.toml --limit 20
blog backup verify --config config.toml --archive /data/site/backups/site.tar.gz
blog backup drill --config config.toml --archive /data/site/backups/site.tar.gz
blog upgrade prepare --config config.toml
blog restore --config config.toml --archive /data/site/backups/site.tar.gz --target-data-dir /data/isolated
```

`backup create` 使用一致性快照和逐文件 checksum；本地备份包含数据库、媒体、认证秘密、主题和插件数据，不包含缓存和其他备份。`backup drill` 只恢复到临时目录并标记演练时间，不替换正在运行的站点。

正式替换前必须停止应用并确认归档已验证：

```bash
docker compose stop app
docker compose run --rm -T app restore \
  --archive /data/site/backups/site.tar.gz \
  --target-data-dir /data/site \
  --replace
docker compose up -d
docker compose exec -T app /blog status
```

`--replace` 会把旧数据放入 `site.before-restore-*` 回滚目录。健康检查、首页和关键公开输出确认无误前，不要删除该目录。

### 8.3 主题、归档和存储迁移

```bash
blog theme install --config config.toml --archive dist/theme.zip
blog theme list --config config.toml
blog theme activate --config config.toml --id org.example.paper --version 1.0.0
blog theme rollback --config config.toml

blog archive export --config config.toml --output dist/content.zip
blog archive verify --config config.toml --archive dist/content.zip
blog archive import --config config.toml --archive dist/content.zip --dry-run
blog archive import --config config.toml --archive dist/content.zip

blog storage migrate --config config.toml --to s3
blog storage migrate --config config.toml --to local
```

主题和归档操作会取得数据锁。归档导入必须先 dry-run；归档格式包含稳定公共 ID、修订正文、媒体引用、重定向和公开站点设置，但不导出密码、token 或 provider secret。

### 8.4 离线导入

```bash
blog import wordpress --config config.toml --input export.xml --dry-run --report report.json
blog import ghost --config config.toml --input ghost.json --dry-run
blog import markdown --config config.toml --input ./content --dry-run --report report.json
blog import markdown --config config.toml --input ./content
blog audit list --config config.toml --limit 50
```

导入器限制输入文件、ZIP 条目、路径和解压总量；首次使用必须查看 dry-run 报告中的 planned、conflicts、warnings 和 duplicate，再执行正式导入。正式导入默认创建草稿，不直接覆盖已发布内容。

## 9. 备份和数据安全规则

- 不要复制运行中的 `blog.sqlite` 而忽略 `-wal`/`-shm`；使用 `backup create` 的一致性流程。
- 恢复、主题切换、归档导入和存储迁移前保留独立备份，并确保没有另一个进程持有数据锁。
- 本地未加密备份包含认证秘密，必须限制为 0600 并存放在受保护介质；需要异地保存时启用加密密钥文件或受控远程存储。
- 不要把 `data/`、数据库、备份、auth key、S3/SMTP/Webhook secret、导入报告和运行产物提交到 Git。
- 任何公开内容变更都应通过应用服务完成，不直接修改 SQLite 表；否则可能漏掉版本、重定向、`render_epoch`、搜索 dirty 标记或事件 outbox。

## 10. 故障排查

### 应用启动但页面打不开

```bash
docker compose ps -a
docker compose logs --tail=200 app
docker compose exec -T app /blog healthcheck --url http://127.0.0.1:8080/readyz
docker compose exec -T app /blog status
```

先区分 app 和 Caddy：app 健康但 Caddy 为 `Created`/`Exited`，通常是 Caddyfile 路径共享、端口占用或证书目录权限问题；app 不健康则先看迁移、数据目录权限和认证秘密。

### 显示数据库被占用或数据锁

确认没有同时运行本地二进制、Compose app、backup/restore/import 或 theme/storage 命令。恢复和离线导入必须停止正式服务，避免两个进程同时写数据目录。

### 登录后立刻回到登录页

检查公开基址协议与 Cookie Secure：

- HTTPS 站点应使用 `BLOG_SITE_ADDRESS=https://...`。
- HTTP 本地开发应使用 `BLOG_BASE_URL=http://...` 或显式 `BLOG_COOKIE_SECURE=false`。
- 反向代理部署时只把实际代理出口网段写入 `BLOG_TRUSTED_PROXY_CIDRS`。

### 搜索结果暂时不完整

搜索是可重建的异步投影。发布成功不依赖搜索同步；等待生命周期任务处理，查看 `/admin/operations/tasks` 和应用日志。若索引损坏，按运维记录执行有界重建，不直接删除正文数据。

### 可选插件没有路由

插件默认关闭。确认 TOML 或环境变量已启用、插件初始化没有失败，并在 `/admin/plugins` 查看状态。停用插件不会删除配置、数据或任务；重新启用后可继续处理保留任务。

## 11. 开发和发布验证

所有 Go 命令都必须使用固定构建标签：`fts5 sqlite_omit_load_extension`。

```bash
make test
make test-race
make vet
go mod verify
make build
git diff --check
make perf-gate
make stage3-acceptance
```

浏览器回归：

```bash
make browser
BROWSER_STRICT=1 make browser
```

严格模式要求 Playwright、Chromium、Firefox 和 WebKit 已安装；没有浏览器时不能把 Go HTTP 测试当作浏览器验收。

发布检查：

```bash
make sbom
make license-audit
make release
```

`make release` 默认生成本地多架构 OCI 归档、SBOM/许可证报告和 `SHA256SUMS`；只有明确需要推送镜像时才设置 `PUSH=1`。

当前验证边界应以 [阶段四实施记录](./progress/phase-four-product-and-public-capabilities.md) 和 [阶段三性能报告](./progress/phase-three-performance-implementation.md) 为准：代码级测试已覆盖主要能力，但 Caddy/TLS、严格浏览器、目标容器资源压测、真实封面媒体容量和部分隔离恢复证据需要在相应环境补验。

### 主题删除与重新添加

后台主题页允许删除未启用的已安装主题。删除以 `themes.removed_at` 标记，从已安装列表移除，保留主题包、设置和切换历史；不能删除当前主题或内嵌默认回退主题。删除需要登录、CSRF 校验与显式确认，重复操作不会重复写审计记录。已删除主题不能启用、预览或修改设置；重新添加会重新校验包、目录校验和与设置，且不会自动启用。若回退历史指向已删除主题，则使用内嵌默认主题。主题删除不会释放磁盘空间。
