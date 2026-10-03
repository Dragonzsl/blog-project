# 个人博客系统

这是一个面向公开复用的单站点自托管博客系统。阶段一核心闭环、阶段二扩展能力与阶段三生态/发行加固已经完成；后续生产安全、性能、公共能力和产品化改进已按阶段持续实施，当前实现、使用方式和技术细节统一见[文档索引](./docs/README.md)、[项目使用与运维手册](./docs/usage.md)和[实现级架构与技术细节](./docs/architecture-implementation.md)。阶段记录见[阶段一核心闭环](./docs/progress/stage-1.md)、[阶段二](./docs/progress/stage-2.md)、[阶段三](./docs/progress/stage-3.md)、[改进阶段一](./docs/progress/phase-one-production-hardening.md)和[改进阶段四](./docs/progress/phase-four-product-and-public-capabilities.md)。

核心方向：Go 模块化单体、SQLite、Markdown、服务端渲染、可上传主题、可信编译期插件，以及可在 1 核 1 GiB VPS 上稳定运行的硬性资源预算。

## 设计文档

- [文档索引](./docs/README.md)
- [项目使用与运维手册](./docs/usage.md)
- [实现级架构与技术细节](./docs/architecture-implementation.md)
- [领域词汇表](./CONTEXT.md)
- [技术选型](./docs/technical-selection.md)
- [总体架构](./docs/architecture.md)
- [数据模型](./docs/data-model.md)
- [主题与插件契约](./docs/extensions.md)
- [主题开发手册](./docs/development/themes.md)
- [插件 Host API](./docs/development/plugins.md)
- [视图模型参考](./docs/reference/view-models.md)
- [事件版本策略](./docs/reference/events.md)
- [功能范围与交付路线](./docs/product-and-roadmap.md)
- [主流博客系统对照](./docs/research/popular-systems.md)
- [博客界面重构计划](./docs/ui-refactor-plan.md)
- [公开阅读端阶段二实施记录](./docs/progress/ui-stage-2.md)
- [架构决策索引](./docs/adr/README.md)

## 一句话架构

公开请求由 Caddy 终止 TLS 后进入一个 Go 应用；Go 应用同时承载公开页面、管理后台、任务与 CLI 模式，使用同进程 SQLite 和本地持久卷，S3、邮件及外部集成均为可选适配器。

## 运行当前版本

当前实现已覆盖唯一站主安全初始化、文章与页面、默认主题、Markdown 安全渲染、分类标签与导航、本地媒体、不可变版本、15 秒编辑快照、定时发布、撤回、30 天回收站、中英文搜索、SEO、RSS、Sitemap、robots、llms.txt、永久重定向，以及可校验备份、原子恢复、恢复演练、升级恢复点和运维审计。阶段二新增受限主题包预览/切换/回退、可禁用插件宿主、本地或外部评论、本地隐私统计、S3/SMTP/Newsletter 适配、Markdown 内容归档和媒体校验迁移；阶段三新增 WordPress/Ghost/Markdown 离线导入与 dry-run、只读内容 API、签名 Webhook、开发者契约文档、amd64/arm64 发行脚本、SBOM/许可证审查、ADR-0031 性能门和三浏览器回归夹具；后续改进阶段补充了站点/站主设置、版本对比与恢复预览、批量内容操作、排期日历、评论与 Newsletter 管理、游标/增量内容 API、SEO/社交元数据、Feed 摘要模式和 v2 归档能力。需要 Docker Desktop 或 Docker Engine + Compose：

```bash
./scripts/deploy.sh
```

脚本首次运行会从 `.env.example` 创建权限受限的 `.env`，以后重复运行会构建、升级并等待应用及公开入口就绪（需要 curl）；如果检测到已有运行中的应用，默认会先创建升级前恢复点。部署脚本只使用 `compose.yaml`，不会加载本地覆盖配置；正式域名部署前请先编辑 `.env` 中的 `BLOG_SITE_ADDRESS`，也可以运行 `make deploy`。

本地健康检查仍可手动执行：

```bash
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

随后访问 `https://localhost/admin/setup`，创建唯一站主并绑定任意兼容 TOTP 的验证器。初始化完成时会显示 10 枚单次恢复码；系统不会再次保存或展示其明文。

本地 `localhost` 使用 Caddy 内部证书，因此命令行演示带 `--insecure`；部署到已解析的公开域名时，将 `BLOG_SITE_ADDRESS` 改为实际 HTTPS 地址，Caddy 会自动申请证书。运行数据保存在 `blog_data` 命名卷的 `/data/site` 子目录中，使恢复流程可以原子切换整个站点数据；普通停止不会删除数据：

```bash
docker compose down
```

`BLOG_SITE_ADDRESS` 同时作为 canonical、Open Graph、RSS、Sitemap、robots 和 llms.txt 的公开基址，正式部署前必须设置为访问者实际使用的地址。内容编辑器可单独覆盖 SEO 标题和描述；留空时自动回退到内容标题、摘要与站点名。

应用只信任 `BLOG_TRUSTED_PROXY_CIDRS` 中列出的反向代理网段提供的 `X-Forwarded-For`。默认 Compose 已将 Caddy 与应用绑定在 `172.30.0.0/24` 内部网段；如果前面还有 Nginx、平台负载均衡器或其他代理，请将环境变量替换为实际代理出口网段，不要填入 `0.0.0.0/0`。未配置时会安全地使用应用看到的直接对端地址。

Newsletter 启用后，公开订阅先进入待确认状态；确认邮件、外部同步和通知都由后台任务处理。管理员可在 `/admin/operations/tasks` 查看 pending/running/failed 任务、脱敏错误并安全重试失败任务。

Cookie 的 Secure 属性默认跟随 `BLOG_SITE_ADDRESS` 的协议：HTTPS 自动启用，HTTP 本地开发自动关闭；如有特殊部署需求，可通过 `BLOG_COOKIE_SECURE` 显式覆盖。

### 阶段二可选能力

所有扩展默认关闭，不配置外部服务也能完整运行。管理员可在 `/admin/plugins` 启停已编译的官方插件；主题包在 `/admin/themes` 上传、预览并原子切换。评论、统计和 Newsletter 启用后分别提供受保护审核/统计页面及公开接口。S3、SMTP 和外部 Newsletter 的凭据建议通过 `.env` 注入，具体命令与迁移流程见[阶段二实施记录](./docs/progress/stage-2.md)。

开发机已安装 Go 1.26 和 C 编译器时，可运行：

```bash
make test
make build
./bin/blog serve --config config.example.toml
```

### 阶段三导入与集成

```bash
docker compose exec -T app /blog import wordpress --input /data/site/import/export.xml --dry-run --report /data/site/import/report.json
docker compose exec -T app /blog import ghost --input /data/site/import/ghost.json
docker compose exec -T app /blog import markdown --input /data/site/import/content --dry-run
make perf-gate
make stage3-acceptance
```

只读 API 和签名 Webhook 默认关闭；配置示例见 `config.example.toml` 与 `.env.example`。发布机可以运行 `make release` 生成多架构 OCI、SBOM、许可证审查和 `SHA256SUMS`；完整安全/恢复说明见[阶段三审计](./docs/security/stage-3-audit.md)。

发行构建必须使用 `fts5 sqlite_omit_load_extension` 标签；Makefile 和 Dockerfile 已固定这些标签。应用容器以非 root、只读根文件系统运行，默认限制为 0.85 CPU、256 MiB，Go 堆软上限为 192 MiB；Caddy 默认限制为 64 MiB。该预算仍为 Argon2id 的单并发 64 MiB 工作区保留余量。

### 无邮件认证恢复

服务器管理员可以从标准输入或权限受限文件提供新密码。命令会轮换密码、TOTP 和恢复码，并使全部旧会话失效：

```bash
printf '%s\n' 'your-new-password' | docker compose run --rm -T app auth recover --username owner --password-file -
```

命令输出新的 TOTP URI、手动密钥和一次性恢复码，请立即离线保存。密码至少 12 个字符，不应直接写入命令行参数。若未设置 `BLOG_AUTH_SECRET`，应用首次启动会在数据卷内生成权限为 `0600` 的 `secrets/auth.key`；它用于加密 TOTP 密钥，不会出现在普通站点配置中。

### 备份、演练与恢复

应用默认每 24 小时创建一份一致性备份，并保留 7 个每日点和 4 个每周点；手动及升级前备份不受自动保留策略删除。备份使用 SQLite `VACUUM INTO` 快照和逐文件 SHA-256 清单，包含数据库、媒体、认证秘密、主题和插件，不包含缓存及其他备份：

```bash
docker compose exec -T app /blog backup create
docker compose exec -T app /blog backup list
docker compose exec -T app /blog status
docker compose exec -T app /blog audit list
docker compose exec -T app /blog upgrade prepare
docker compose exec -T app /blog backup drill --archive /data/site/backups/<文件名>.tar.gz
```

完整恢复必须先停止正式应用，避免绕过数据目录锁。`--replace` 会原子切换数据目录，并把原数据保存在命令输出的 `site.before-restore-*` 回滚目录中：

```bash
docker compose stop app
docker compose run --rm -T app restore --archive /data/site/backups/<文件名>.tar.gz --replace
docker compose up -d
docker compose ps
```

恢复完成后，所用归档位于旧数据的回滚目录内，因为备份不会递归包含备份文件；确认站点健康并另行保存归档前，不要删除该回滚目录。备份文件权限为 `0600`。本地归档默认不加密且包含认证秘密；当前实现支持按配置启用归档加密和 S3 远端归档，但两者都必须使用受保护的密钥/凭据并单独完成恢复演练，具体配置、失败语义和边界见[项目使用与运维手册](./docs/usage.md)与[实现级架构与技术细节](./docs/architecture-implementation.md)。

## 当前约束

- 每次部署只有一个站点和一个站主，不做角色、多租户或读者账户。
- 文章与页面正文以数据库中的 Markdown 为权威版本。
- 公开页面优先走可缓存快照，动态能力走独立接口。
- 主题可上传但不能执行服务端代码；插件随程序编译并按配置启停。
- 核心 1.0 功能按三个可运行的纵向阶段交付，后续改进阶段继续补齐生产加固、性能验证、公共能力和产品化运营功能。
