# 阶段二实施记录

阶段二把主题、插件和外部能力接入到阶段一的模块化单体，同时保持单站主、SQLite 和低常驻开销边界。可选能力默认关闭；插件异常只记录在宿主状态中，不阻塞文章发布。

## 已实现范围

| 能力 | 实现 | 验证 |
|---|---|---|
| 主题包安装/验证/预览/切换/回退 | `internal/presentation`、`/admin/themes`、`blog theme` | ZIP 路径/大小/文件数/模板/API/semver 校验；原子目录发布；损坏主题回退内嵌默认主题 |
| 插件宿主与设置 | `internal/extensions`、`/admin/plugins` | API 版本、能力边界、设置 schema、菜单、事件、幂等任务；受保护后台路由 |
| 本地/外部评论 | `internal/comments` | Markdown 清洗、匿名提交限流、一级回复、审核；`comment_provider` 互斥 |
| 本地隐私统计 | `internal/analytics`、统计看板 | HMAC 访客摘要、按日聚合、保留期清理；默认不记录 |
| S3/SMTP/Newsletter 适配 | `internal/media`、`internal/notifications` | 原子对象写入、S3 兼容 HTTP 与 SigV4、SMTP STARTTLS、加密本地订阅、外部 JSON 适配 |
| 归档、媒体迁移、重定向 | `internal/archive`、`blog archive/storage`、`organization` | Markdown ZIP 导出/校验/导入；媒体逐对象大小+SHA-256 校验并可逆迁移；站内 301/308 管理 |

## 低资源策略

- Go 应用仍为单进程；禁用插件不注册公开/后台插件路由，宿主仅保留少量状态行。
- 统计只写日聚合表和 HMAC 摘要，不保存原始 IP；保留任务复用已有生命周期循环。
- S3 上传先以本地临时文件完成 MIME/像素校验，再按对象写入远端；原文不会被有损压缩，图片派生图仍使用可配置 JPEG 质量/PNG 压缩。
- 外部网络调用只发生在明确启用的存储、邮件、Newsletter 或任务适配器中；发布事务不等待这些调用。

## 配置示例

```toml
[storage]
adapter = "local" # 或 s3

[analytics]
enabled = false
retention_days = 365

[comments]
enabled = false
provider = "local" # local / external / disabled
require_moderation = true

[mail]
enabled = false
starttls = true

[newsletter]
enabled = false
provider = "disabled" # local 或外部 provider 名
```

秘密值优先使用环境变量（`BLOG_S3_SECRET_KEY`、`BLOG_MAIL_PASSWORD`、`BLOG_NEWSLETTER_TOKEN`），不写入普通站点配置或日志。

## 离线命令

```bash
blog theme install --archive theme.zip
blog theme list
blog theme activate --id paper --version 1.0.0
blog theme rollback

blog archive export --output content.zip
blog archive verify --archive content.zip
blog archive import --archive content.zip

blog storage migrate --to s3
blog storage migrate --to local
```

归档导入始终创建草稿，避免未经确认直接覆盖已发布内容；媒体迁移遇到校验失败时保留源位置索引，可安全重试。

## 验收记录

- 自动化：`go test -tags "fts5 sqlite_omit_load_extension" ./...`、race、`go vet`、`go mod verify` 和 `git diff --check` 均应通过。
- 数据库从阶段一版本 8 向前迁移到版本 9；旧数据位置会由 `media_storage_locations` 补齐本地索引。
- 浏览器验收覆盖管理员登录后的插件/主题/重定向页面，以及启用评论/统计后的公开接口；桌面和移动宽度均不得出现横向溢出。
- 资源验收继续使用 Compose 的应用 256 MiB、Caddy 64 MiB 上限；空载不启用可选插件，启用后只增加对应请求/任务开销。

## 剩余项

阶段三再处理 WordPress/Ghost 导入、只读 API、签名 Webhook、主题/插件开发者文档、跨平台发行物和完整浏览器回归矩阵。阶段二不扩展角色、读者账户或多租户模型。
