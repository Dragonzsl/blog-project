# 只读内容 API v1

Content API 是可选插件 `contentapi.readonly`。插件未启用时所有 `/api/v1/*` 路由都不存在；启用后仍然只读，公开文章和页面只读取已发布修订版本。

## 列表

```text
GET /api/v1/posts
GET /api/v1/pages
```

默认使用稳定游标分页：`per_page` 默认为 20，最大 100。响应包含 `next_cursor`、`has_more` 和 `protocol_version`。下一页只把游标原样传回：

```text
GET /api/v1/posts?per_page=50&cursor=<next_cursor>
```

兼容旧调用方的页码分页仍可使用 `page`，但 `page` 不能与 `cursor` 同时出现。游标由服务端生成，修改或伪造会得到 400；游标包含发布时间和稳定 `public_id`，不会暴露 SQLite 自增 ID。

列表可以按分类、标签和发布时间过滤：

```text
GET /api/v1/posts?category=go&tag=sqlite&updated_since=2026-09-01T00:00:00Z
```

`updated_since` 针对已发布修订时间，适合增量同步；时间不能早于当前时间往前 10 年，也不能超过当前时间 5 分钟。过滤参数、每页大小和请求体都设有上限；接口不提供草稿、编辑快照、认证密钥、邮箱或内部数据库字段。

## 单项

```text
GET /api/v1/posts/{slug}
GET /api/v1/pages/{slug}
GET /api/v1/site
```

单项返回稳定 `id`、标题、摘要、Markdown 正文、公开 URL、分类、标签、SEO 字段以及可用的公开封面信息。列表是有界的轻量卡片投影，不返回 `body_markdown`，避免同步大正文时放大内存和响应体；需要正文时按 slug 请求单项接口。

## 缓存与认证

成功的公开响应带有 ETag、Last-Modified 和短时 `public` 缓存策略，可以使用 `If-None-Match` 或 `If-Modified-Since` 获取 304。配置 `CONTENT_API_TOKEN` 后，客户端必须使用 `Authorization: Bearer <token>`；认证响应使用 `private, no-store`，令牌比较为常量时间比较。

读取接口按可信代理解析后的客户端身份做有界限流，默认每分钟 120 次；超过限制返回 429 和 `Retry-After: 60`。限流键、窗口和条目数量均有内存上限，不能通过伪造未受信任的转发头绕过。

API token、站点密钥和插件配置不会出现在 JSON、错误消息、审计记录或日志中。接口不承诺删除墓碑；增量同步方应以稳定 slug/id 对本地记录执行覆盖或重建，并定期做全量同步。

游标是带服务端签名的 opaque 值，并绑定内容类型、每页大小、分类、标签和 `updated_since` 条件。篡改、过期或跨筛选条件复用都会返回 400；游标有效期为 24 小时。
