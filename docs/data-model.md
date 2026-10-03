# 数据模型

结构以 [`db/migrations`](../db/migrations) 为准，本页说明主要关系。当前迁移编号为 `00001`–`00022`。

## 标识与时间

内部关系使用 SQLite 整数主键。内容、修订、媒体等公共对象另有 16 字节 `public_id`：前 6 字节为 UTC 毫秒时间，后 10 字节为随机数，外部编码为 32 位十六进制。公共 URL 使用 slug 或公共 ID，不使用内部行号。

时间以 UTC Unix 毫秒持久化，展示和排期输入使用站点时区。导入来源与指纹用于去重，不替代本系统身份。

## 站点与身份

`sites` 和 `owners` 通过 `id=1` 约束单站点、单站主。站点保存名称、语言、时区、公开基址和后续迁移增加的 SEO 等设置。渲染版本位于 `system_state.render_epoch`，不在 `sites` 中。

站主密码为 Argon2id 哈希，TOTP 密钥加密。`sessions` 保存令牌哈希、认证版本和时间；`owner_recovery_codes` 保存恢复码哈希和使用时间。初始化与安全变更挑战分别使用 `owner_setup_challenges`、`owner_security_challenges`。

## 内容与修订

`contents` 共用文章和页面状态：

- `kind` 为 `article` 或 `page`，`status` 为 `draft`、`scheduled` 或 `published`。
- 保存当前标题、摘要、Markdown、slug、SEO、分类、封面与时间。
- `current_revision_id` 指向编辑版本，`published_revision_id` 指向公开版本。
- `published_slug` / `published_slug_key` 保留发布路径，`lock_version` 处理并发编辑。
- `trashed_at` 与发布状态分开，回收站内容不进入公开查询。

`content_revisions` 保存完整修订快照和原因，含分类/标签公共身份、SEO 和封面快照。恢复创建新修订，不改旧正文。发布检查点不受普通修订数量上限淘汰。历史封面快照版本 `0` 表示未记录，`1` 表示有记录，空封面表示该修订明确无封面。

`editing_snapshots` 每项内容最多一个可覆盖的编辑快照，不是正式修订，不能公开读取。`bulk_operations` 保存批量操作的幂等结果。

## 组织与路径

文章最多一个分类，通过 `contents.category_id` 关联 `categories`；`content_tags` 关联多个标签。页面不能拥有分类和标签。

`navigation_menus` 的位置为 `primary`、`footer`；`navigation_items` 支持内容、分类、标签和外部 URL，最多一层子项。

`reserved_paths` 保留系统、草稿、发布和历史路径，`redirects` 保存旧路径到目标的重定向。已发布 slug 改动在同一事务保留旧路径、登记重定向并使公开缓存失效。

## 媒体

`media` 保存公共身份、原名、检测 MIME、大小、尺寸、哈希与替代文字；`media_variants` 保存变体信息。`media_storage_locations` 记录原图和变体的当前适配器、对象键与校验信息，`storage_migrations` 记录迁移游标和租约。

`media_references` 记录正文/封面引用。删除操作依据引用表拒绝删除在用媒体；引用重建包含当前编辑内容和已发布正文/封面。不要假设所有历史修订或主题设置都具备相同的删除保护。对象键不向主题视图暴露；内容哈希不用于自动合并媒体身份。

## 派生数据与任务

| 表 | 用途 |
|---|---|
| `search_documents`、`search_grams`、`search_dirty` | 已发布文本的 FTS5、中文 gram 与待同步标记 |
| `public_taxonomy_members`、`public_taxonomy_rebuild_state` | 分类/标签发布投影及重建进度 |
| `system_state` | 单调递增渲染版本；页面缓存主体在文件系统 |
| `jobs` | 类型、载荷版本、幂等键、状态、可见时间、租约、尝试次数和最近耗时/错误 |
| `event_outbox` | 业务事务内登记的事件；派发任务只引用事件 ID |
| `request_idempotencies`、`public_write_fingerprints` | 公开写入的有限结果摘要与短期重复指纹 |
| `audit_entries` | 管理动作、结果与脱敏上下文 |
| `backups` | 归档目标、大小、验证、加密和演练状态，不保存解密密钥 |
| `import_runs` | 导入来源、指纹、结果和报告 |

缓存、搜索、taxonomy 和媒体变体可重建；统计原始请求不会完整保存，不能由聚合数据恢复。

## 主题与插件

`themes` 保存包、版本、路径、校验、激活和移除状态；唯一索引限制一个活动主题。`theme_settings` 保存 schema 版本与值，`theme_activation_history` 记录切换历史。`active.json` 是可修复标记。

`plugin_states` 保存版本、启用、初始化结果和 `removed_at`；`plugin_settings` 保存 schema 版本与 JSON。敏感设置在管理读取时脱敏，这不代表 JSON 中所有值都经过加密。外部服务凭据应通过受保护的启动配置提供。

主题/插件移除均保留数据。重新添加不自动启用；主题文件不会因后台移除而删除。

## 可选能力的数据

- `comments`：父评论、状态、正文、清洗 HTML、加密邮箱和邮箱摘要。
- `analytics_daily`：按日期和路径的访问聚合；`analytics_visitor_days`：日期与访客 HMAC，用于去重，不保存原始 IP。没有月度聚合表。
- `newsletter_subscribers`：加密邮箱、版本化 HMAC 和订阅状态；`newsletter_tokens` 保存确认/退订摘要，异步邮件所需 token 以受保护密文保存。
- `notification_outbox`：收件人、主题、消息及发送状态，属于私有数据。
- `webhook_deliveries`：事件键、投递与重试状态，事件键有唯一约束。

这些私有表也会进入完整备份；内容归档只导出允许公开的内容和设置。

## 一致性与保留

公开读取必须同时满足已发布、未回收、存在发布修订。改变公开内容、主题、导航或站点设置时增加 `render_epoch`。业务状态、事件和任务同事务登记，外部副作用在提交后执行。

默认编辑快照覆盖保存，普通修订保留 50 个，回收站保留 30 天，统计保留 365 天，自动备份保留 7 个每日点和 4 个每周点。不要假设审计与成功任务已有统一的一年自动清理策略。实际索引、约束与清理范围须查看迁移和对应服务，新增索引应有查询依据。
