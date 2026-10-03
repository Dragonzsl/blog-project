# 使用与运维

## 安装

需要 Docker Engine 或 Docker Desktop、Compose v2 和 curl，宿主机 80、443 端口可用。

```bash
./scripts/deploy.sh
```

首次在终端运行时会提示输入域名，例如 `blog.example.com`，也可输入 `https://blog.example.com`；留空使用 `localhost`。脚本从 `.env.example` 创建权限为 `0600` 的 `.env`，写入 HTTPS 地址，构建镜像并启动服务。成功条件是应用内部就绪、公开地址的 `/readyz` 和首页均返回 HTTP 200。默认等待 180 秒，可用 `--wait` 调整。

已有 `.env` 时沿用配置，不会重复询问或覆盖其他配置。首次用脚本创建后，中途构建失败也可重新执行。显式改域名只更新 `BLOG_SITE_ADDRESS`，保留密钥和其他设置：

```bash
./scripts/deploy.sh --configure
./scripts/deploy.sh --domain blog.example.com
```

`--domain` 也适用于 CI/脚本；`--non-interactive` 不询问输入，首次未指定域名时使用 `localhost`。域名不支持路径、端口、通配符或 IP 地址，中文域名应使用 Punycode。非终端环境默认不询问，不会卡在输入步骤。

脚本检查 Docker daemon 和 Compose v2 是否可用。Docker 需先安装并启动；脚本不安装系统软件，也不修改 DNS 或防火墙。

脚本只加载 `compose.yaml`，不读取 `compose.override.yaml` 或 `COMPOSE_FILE`。再次执行时，若应用正在运行，先创建升级前恢复点。选项见 `./scripts/deploy.sh --help`；`--no-build` 使用现有镜像，`--no-backup` 跳过恢复点。

成功后脚本输出站点地址、初始化地址和登录地址。选择 `localhost` 时入口为 `https://localhost`，使用 Caddy 内部证书。检查本地服务：

```bash
docker compose -f compose.yaml ps
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

正式域名部署前，将 A/AAAA 记录指向服务器并开放 TCP 80、443；注意删除指向其他服务器的旧 AAAA 记录。脚本把输入域名配置为 `BLOG_SITE_ADDRESS=https://域名`，Caddy 自动申请和续期证书，同时将 HTTP 重定向到 HTTPS，见 [Caddy 自动 HTTPS](https://caddyserver.com/docs/automatic-https)。正式域名的健康检查不要使用 `--insecure`。脚本不跟随重定向，只对 `https://localhost` 跳过证书校验。

默认应用限制为 0.85 CPU / 256 MiB，Go 堆软上限 192 MiB；Caddy 为 0.15 CPU / 64 MiB。`data-init` 初始化数据目录后退出。

## 首次初始化

访问 `/admin/setup`，设置站点名、用户名和至少 12 个字符的密码，绑定 TOTP 验证器并离线保存一次性恢复码。之后使用密码和 TOTP 登录 `/admin/login`。

每次部署只有一个站主。恢复码只在生成时显示，丢失认证信息后可通过服务器 CLI 恢复，见下文。

## 本地运行

需要 Go 1.26 和 C 编译器：

```bash
make build
./bin/blog serve --config config.example.toml
```

访问 `http://localhost:8080`。本地二进制不会自动加载 `.env`；使用 TOML 或显式环境变量配置：

```bash
BLOG_BASE_URL=http://localhost:8080 ./bin/blog serve --config config.example.toml
```

## 配置

加载顺序为内置默认值 → TOML → 环境变量 → 路径解析与校验。`--config` 指定 TOML，未指定时读取 `BLOG_CONFIG_FILE`。相对数据目录以配置文件所在目录为基准，数据库、媒体和密钥位于数据目录内。

完整字段见 [`config.example.toml`](../config.example.toml)，环境变量示例见 [`.env.example`](../.env.example)。

| 配置 | 默认值 | 作用 |
|---|---|---|
| `server.listen_address` | `:8080` | 应用监听地址 |
| `storage.data_dir` | `./data` | 数据目录；Compose 为 `/data/site` |
| `database.read_connections` | `2` | 读连接数，范围 1–8；写连接固定为 1 |
| `discovery.base_url` / `BLOG_BASE_URL` | `http://localhost:8080` | 应用公开基址 |
| `BLOG_SITE_ADDRESS` | `https://localhost` | Compose 的 Caddy 地址，并映射为应用 `BLOG_BASE_URL` |
| `server.trusted_proxy_cidrs` | 空 | 可信代理；Compose 默认为 `172.30.0.0/24` |
| `media.max_upload_bytes` | `12 MiB` | 上传上限 |
| `media.max_image_pixels` | `16,000,000` | 图片像素上限 |
| `publishing.editing_snapshot_interval` | `15s` | 编辑快照间隔 |
| `publishing.revision_limit` | `50` | 普通修订保留数，发布检查点另行保护 |
| `publishing.trash_retention_days` | `30` | 回收站保留天数 |
| `operations.backup_interval` | `24h` | 自动备份间隔 |
| `security.session_lifetime` | `12h` | 会话有效期 |

`BLOG_SITE_ADDRESS` 影响证书和公开 URL。后台 `/admin/settings` 保存过公开基址后，数据库中的值用于站点输出；更换域名时应同步更新后台设置与部署环境。

Compose 传入 `BLOG_BASE_URL` 时，Cookie Secure 默认跟随其协议。直接使用 TOML 时检查 `security.cookie_secure`；HTTP 本地开发为 false，HTTPS 部署应为 true。需要显式覆盖时传入 `BLOG_COOKIE_SECURE`。

应用只信任直接对端命中可信 CIDR 的转发头。新增代理时配置实际出口网段，不要使用 `0.0.0.0/0`。

### Compose 的可选配置

`.env` 是 Compose 的变量替换来源，不会自动把所有变量传入容器。当前 `compose.yaml` 只映射监听地址、数据目录、日志、认证密钥、公开基址、可信代理和 Go 内存上限。仅在 `.env` 填写 `BLOG_MAIL_PASSWORD`、`BLOG_S3_SECRET_KEY` 或插件开关不会生效。

可用单独的本地覆盖文件显式传入所需变量。例如启用带 token 的内容 API，新建 `compose.features.yaml`：

```yaml
services:
  app:
    environment:
      BLOG_CONTENT_API_ENABLED: "true"
      BLOG_CONTENT_API_TOKEN: ${BLOG_CONTENT_API_TOKEN:?请在受保护的环境中设置 token}
```

在 `.env` 中设置随机 token 后，使用明确的文件列表：

```bash
# 已运行的站点先创建升级前恢复点
docker compose -f compose.yaml -f compose.features.yaml exec -T app /blog upgrade prepare
docker compose -f compose.yaml -f compose.features.yaml up --build -d
```

首次启动跳过恢复点命令。此部署之后的停止、升级和 CLI 命令也应使用相同文件列表；不要改用只加载基础文件的 `deploy.sh`，否则附加配置会丢失。保存凭据的环境文件和本地覆盖文件不得提交。

也可挂载受限 TOML 文件，并显式传入 `BLOG_CONFIG_FILE`，或通过文件提供密钥；Docker secret 不会自动映射到应用配置。

## 站点与后台

公开路径包括 `/`、`/articles`、`/posts/{slug}`、`/{页面slug}`、`/archive`、`/categories`、`/tags` 和 `/search`。机器可读入口为 `/rss.xml`、`/sitemap.xml`、`/robots.txt`、`/llms.txt`。

公开读取只使用已发布修订。修改已发布内容的 slug 会保留旧路径重定向；保存草稿和编辑快照不会直接替换公开正文。

| 后台入口 | 用途 |
|---|---|
| `/admin/articles`、`/admin/pages` | 编辑、筛选、发布、排期、版本和批量操作 |
| `/admin/organization`、`/admin/redirects` | 分类、标签、导航与重定向 |
| `/admin/media` | 上传、替代文字、引用和删除保护 |
| `/admin/themes` | 安装、设置、预览、激活、回退和移除主题 |
| `/admin/plugins` | 查看、启停、移除和重新添加插件 |
| `/admin/settings` | 站点、SEO、Feed 与站主安全设置 |
| `/admin/operations/tasks` | 任务状态、脱敏错误与失败重试 |

主题移除保留包、设置和历史，不释放磁盘空间；当前主题和内嵌默认主题不能移除。插件移除保留配置、数据与任务，重启不会自动恢复。重新添加后不会自动启用。

## 可选能力

| 能力 | TOML 配置 | 说明 |
|---|---|---|
| 评论 | `comments.enabled=true`，`provider=local` 或 `external` | 本地审核或外部入口，两者互斥 |
| 隐私统计 | `analytics.enabled=true` | 本地聚合，后台 `/admin/analytics` |
| Newsletter | `newsletter.enabled=true`，`provider=local` 或 `external` | 确认订阅、退订与任务；外部服务需 endpoint/token |
| 内容 API | `content_api.enabled=true` | 只读已发布数据，见 [API 参考](api/content-api-v1.md) |
| Webhook | `webhooks.enabled=true` | 提交后签名投递，需要 endpoint/secret |
| S3 媒体 | `storage.adapter=s3` | S3 兼容对象存储 |
| SMTP | `mail.enabled=true` | 异步通知，需邮件服务器配置 |

`BLOG_CONTENT_API_TOKEN`、`BLOG_NEWSLETTER_TOKEN`、`BLOG_WEBHOOK_SECRET` 从环境变量读取，不能写成 TOML 字段。Newsletter 还需在 TOML 设置 `enabled=true`；当前没有对应的环境开关。插件页面不能代替启动配置中的外部凭据。网络适配器的现有限制见[实现说明](architecture-implementation.md)。

## CLI

使用二进制 `blog <命令>`；Compose 通常为 `docker compose -f compose.yaml exec -T app /blog <命令>`。涉及独占数据锁的命令应停止服务后用 `run --rm -T app` 执行。各子命令选项用 `--help` 查看。

```bash
blog version
blog status --config config.toml
blog migrate --config config.toml
blog healthcheck --url http://127.0.0.1:8080/readyz
blog audit list --config config.toml --limit 50
```

`migrate` 打开数据库、应用向前迁移并输出版本，不是回滚工具。

### 备份与恢复

默认每 24 小时备份，保留 7 个每日点、4 个每周点；手动和升级前恢复点不受自动保留策略删除。备份使用 SQLite 一致性快照，包含数据库、媒体、主题、插件数据和认证秘密，不含缓存或其他备份。

```bash
docker compose -f compose.yaml exec -T app /blog backup create
docker compose -f compose.yaml exec -T app /blog backup list
docker compose -f compose.yaml exec -T app /blog backup verify --archive /data/site/backups/site.tar.gz
docker compose -f compose.yaml exec -T app /blog backup drill --archive /data/site/backups/site.tar.gz
docker compose -f compose.yaml exec -T app /blog upgrade prepare
```

将 `site.tar.gz` 替换为实际归档文件名。`backup drill` 恢复到隔离目录，不替换当前实例。备份默认未加密，文件权限为 `0600`；可配置独立加密密钥文件和 S3 备份目标。异地副本必须单独验证和演练。

恢复前验证归档并停止应用：

```bash
docker compose -f compose.yaml stop app
docker compose -f compose.yaml run --rm -T app restore   --archive /data/site/backups/site.tar.gz --replace
docker compose -f compose.yaml up -d
docker compose -f compose.yaml exec -T app /blog status
```

`--replace` 保留旧数据于命令输出的 `site.before-restore-*` 目录。若归档原来在旧数据目录中，恢复后也在该回滚目录内。检查健康、首页与内容，并另行保存归档后再处理旧目录。

不要直接复制活跃 SQLite 主文件并忽略 WAL/SHM。`docker compose -f compose.yaml down` 保留卷，`down -v` 会删除卷。

### 认证恢复

使用权限为 `0600`、内容为新密码的文件，避免密码进入 shell 历史。命令会轮换密码、TOTP 和恢复码，撤销旧会话。

```bash
docker compose -f compose.yaml stop app
docker compose -f compose.yaml run --rm -T   -v "$PWD/recovery-password.txt:/run/recovery-password:ro"   app auth recover --username owner --password-file /run/recovery-password
docker compose -f compose.yaml up -d
```

将用户名替换为实际站主。上述命令由宿主机读取文件，通过标准输入传入容器。输出含新的 TOTP 密钥和恢复码，请离线保存，及时删除密码文件，不要提交或粘贴到公开日志。

### 主题、归档与导入

以下为本地二进制示例，使用同一配置和数据目录前应停止正在运行的服务：

```bash
blog theme install --config config.toml --archive paper.zip
blog theme list --config config.toml
blog theme activate --config config.toml --id org.example.paper --version 1.0.0
blog theme rollback --config config.toml

blog archive export --config config.toml --output content.zip
blog archive verify --config config.toml --archive content.zip
blog archive import --config config.toml --archive content.zip --dry-run
blog archive import --config config.toml --archive content.zip

blog import wordpress --config config.toml --input export.xml --dry-run --report report.json
blog import ghost --config config.toml --input ghost.json --dry-run
blog import markdown --config config.toml --input ./content --dry-run
blog storage migrate --config config.toml --to s3
```

导入前查看 dry-run 的冲突、警告和重复项。WordPress/Ghost/Markdown 导入默认创建草稿。内容归档支持媒体、引用、修订、重定向和公开设置，不包含认证秘密；它不能代替完整备份。存储迁移前先创建独立备份。

## 故障排查

| 问题 | 检查 |
|---|---|
| 页面打不开 | `docker compose -f compose.yaml ps -a` 与 `logs --tail=200 app caddy`；区分应用、端口、DNS 和证书问题 |
| 数据锁冲突 | 停止使用同一数据目录的其他实例，再执行恢复、导入或主题 CLI |
| 登录后返回登录页 | 检查公开协议、Cookie Secure、代理与站点基址是否一致 |
| 搜索暂时缺内容 | 等待后台同步，查看任务和日志；不要删除正文数据 |
| 插件没有路由 | 检查实际容器配置、插件初始化状态和移除状态，不能仅检查 `.env` |
| 备份或外发失败 | 查看 `blog status` 和任务页，确认存储/网络/凭据；修正后重试 |

开发测试与发行命令见[贡献指南](../CONTRIBUTING.md)。历史验收结果见[历史索引](history.md)，不代替当前环境的测试。
