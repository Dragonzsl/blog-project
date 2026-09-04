# 数据模型

本文描述逻辑模型与不变量，不替代最终 SQL 迁移。表名使用复数 snake_case；时间以 UTC Unix 毫秒保存，展示与定时输入使用站点时区。

## 标识策略

SQLite 表使用 `INTEGER PRIMARY KEY` 获得紧凑索引和稳定性能。需要出现在归档、只读 API 或跨实例引用中的对象额外拥有不可变 `public_id`，使用 128 位时间有序随机标识并存为 16 字节 BLOB；永久链接继续以 slug 作为人类可读地址。

不得把内部行号写进公开 URL。导入器保留来源系统和来源 ID 作为幂等指纹，但来源 ID 不成为本系统身份。

## 核心表

### 站点与身份

`sites` 只有一行，保存公开名称、主要语言、时区、基础 URL、渲染版本及结构化站点设置。数据库约束和启动检查共同保证单站点。

`owners` 只有一行，保存登录名、Argon2id 哈希及参数、认证版本和状态。TOTP 密钥使用由运行时 secret 派生的密钥加密；数据库泄露本身不应直接暴露第二因素。

`owner_recovery_codes` 每行保存一个恢复码的哈希、创建时间和使用时间。`sessions` 只保存随机会话令牌的哈希、认证版本、过期时间、最近活动与必要的安全上下文；修改密码或 TOTP 时提升认证版本并批量失效旧会话。

### 文章与页面

`contents` 同时承载文章和页面的当前查询状态：

- `kind`: `article | page`
- `status`: `draft | scheduled | published`
- `slug` 与规范化 `slug_key`
- 当前标题、摘要、Markdown 正文和 SEO 覆盖字段
- `category_id`，仅文章可用
- 封面媒体、发布时间、定时时间、撤回时间
- `trashed_at`，与发布状态正交
- `current_revision_id` 与 `published_revision_id`
- 乐观并发 `lock_version`

文章和页面共表是因为编辑、版本、发布和 SEO 生命周期一致；它不是开放的自定义内容类型表。数据库 CHECK 约束禁止页面拥有分类、标签、评论开关或定时流字段中不适用的组合。

`editing_snapshots` 每项内容最多一行，保存最近自动保存的正文、元数据、浏览器编辑版本和时间。它可以被新一次自动保存覆盖。

`content_revisions` 是不可变完整快照，保存正文、结构化元数据、分类与标签身份、创建原因以及创建时间。恢复版本会创建一条新记录并更新 `contents`，绝不修改旧版本。

正式保存先插入版本，再更新 `contents.current_revision_id`。发布时把同一版本写入 `published_revision_id`；所有公开读取只能使用当前发布状态允许的快照。

### 分类、标签与导航

`categories` 保存唯一 slug、名称、描述和排序。`contents.category_id` 保证文章最多一个分类。

`tags` 保存唯一 slug、名称和描述；`content_tags` 是文章与标签的多对多关系，并有 `(content_id, tag_id)` 唯一约束。页面不能出现在该关系中。

`navigation_menus` 使用核心定义的位置，例如 `primary` 与 `footer`。`navigation_items` 支持内容链接、分类/标签链接和外部 URL，第一版最多一层子项；循环、过深层级和无效内部目标在应用层拒绝。

### 永久链接与重定向

`reserved_paths` 记录系统路径和曾公开使用的路径，路径比较使用规范化 key。`redirects` 保存来源路径、目标路径、状态码和创建原因；新增重定向时解析到最终目标，禁止循环并压平链条。

修改已发布 slug 的同一事务必须：保留旧路径、写入新路径、创建重定向、提高渲染版本。删除内容不允许把历史路径直接转配给另一项内容。

### 媒体

`media` 保存稳定身份、原始名称、检测 MIME、大小、宽高、内容哈希、替代文本、存储适配器和对象键。对象键是内部实现，不出现在文章正文中。

`media_variants` 保存宽度、格式、大小、内容哈希、对象键和生成状态。`media_references` 记录内容/主题配置对媒体的显式引用；删除前必须同时扫描结构化引用和 Markdown AST，不能只做字符串搜索。

同内容哈希只用于提示重复，不自动合并媒体身份。替换原图创建新媒体版本并重新生成变体，不原地覆盖已有缓存对象。

## 发布派生数据

`search_documents` 或等价 FTS5 虚拟表只包含已发布内容的规范化可搜索文本。它是可重建投影，不是正文权威来源。

`render_state` 保存当前单调递增 `render_epoch`、活动主题版本和最近清理位置。页面缓存主体位于磁盘，不把大量 HTML 存入 SQLite。

`jobs` 保存持久任务类型、载荷版本、幂等键、状态、可见时间、租约、尝试次数和最后错误。载荷必须小；大文件使用媒体或备份对象键引用。

`event_outbox` 保存已经在业务写事务中登记、但尚未完成派发的版本化事件。事件拥有稳定的 `event_id` 和去重键；派发任务只引用它，事件本体和业务写入一起提交或回滚。任务达到重试上限后保留为失败状态，不能静默丢弃。

`request_idempotencies` 保存短期的公开写请求结果，`public_write_fingerprints` 保存按站点密钥计算的短期重复请求指纹。两者只保存摘要和有限响应，不保存原始令牌、密码、邮箱或完整请求体；过期数据由有界批次清理。

`audit_entries` 保存站主重要动作、对象公开 ID、结果、请求 ID 和去敏上下文。审计不保存正文、密码、令牌、TOTP 或访客原始 IP。

`backups` 保存备份清单、目标、大小、校验结果、加密状态和恢复演练时间，不保存备份解密密钥。

## 主题与插件

`themes` 保存已安装主题 ID、版本、API 版本、路径、校验和、验证结果和安装时间。只有一个主题处于活动状态。

`theme_settings` 按主题和 schema 版本保存经过验证的值。切换主题不会删除其他主题配置。

`plugin_states` 保存编译进程序的插件 ID、版本、启用状态、配置 schema 版本和最后初始化结果。`plugin_settings` 保存经过宿主 schema 校验的值；秘密只保存引用。

插件拥有的表使用固定前缀并在核心迁移序列中创建。插件停用保留数据；删除数据是独立、显式且需要备份的操作。

## 官方插件表

本地评论插件使用 `comments`，字段包括文章、父评论、状态、显示名、加密邮箱、邮箱摘要、网站、Markdown、清洗后 HTML、提交时间和审核时间。回复深度由应用限制；评论状态为 `pending | approved | spam | trash`，不复用文章生命周期。

统计插件只保存 `analytics_daily`、`analytics_monthly` 等聚合表。按日轮换的访客近似标识不得落库；原始请求信息在内存中聚合后丢弃。

Webhook 插件保存订阅、投递和有限重试状态；签名密钥只存秘密引用。内容 API 插件除自己的访问配置外不复制内容数据。

Newsletter 订阅保存加密邮箱和带版本的密钥摘要；`email_hash_version=2` 使用运行时站点密钥派生的 HMAC，历史摘要由启动时按批次从加密邮箱重算。确认/退订意图只通过短期 token 摘要定位，token 原文不进入数据库、日志或公开响应；为支持提交后异步发送，必要的 token 密文受长度约束并按站点密钥加密保存。

通知 outbox 和 Webhook 投递均保存稳定幂等键、租约、更新时间与有限错误摘要。Webhook 事件键具有唯一约束，避免同一事件重复创建投递；外发失败由 `jobs` 统一退避，永久配置/策略错误直接进入 `failed`，人工重试仍需显式操作。

## 关键索引

- `contents(kind, status, published_at DESC)` 支撑首页与归档。
- `contents(slug_key)` 唯一索引；回收站内容也占用其历史身份。
- `content_tags(tag_id, content_id)` 和反向唯一关系。
- `jobs(status, available_at)` 与租约查询索引。
- `event_outbox(status, created_at)` 支撑提交后事件派发与失败巡检。
- `request_idempotencies(expires_at)`、`public_write_fingerprints(expires_at)` 和 `newsletter_tokens(expires_at)` 支撑有界过期清理。
- `webhook_deliveries(event_key)` 唯一索引和 `(status, available_at)` 任务索引支撑投递幂等与恢复。
- `redirects(source_path_key)` 唯一索引。
- `media(content_hash)` 非唯一索引用于重复提示。
- 评论按 `(content_id, status, created_at)` 查询。
- 审计与统计表按时间索引并执行保留清理。

每个索引必须由真实查询证明。不得为“以后可能筛选”预建宽索引。

## 一致性规则

- `published` 必须有 `published_revision_id` 和不晚于当前时间的 `published_at`。
- `scheduled` 必须有未来 `scheduled_at`；到点发布使用条件更新防止重复。
- `trashed_at` 非空的内容不进入公开查询、搜索、RSS 或 Sitemap。
- 公开页面永远不读取编辑快照。
- 页面不得拥有分类、标签和评论提供方。
- 一个文章同一时间只有一个评论提供方。
- 主题、导航、站点设置或发布状态变化必须提高 `render_epoch`。
- 外部副作用只能在事务提交后执行，并拥有幂等键。
- 业务事务登记的事件必须先进入 `event_outbox`，再由持久任务派发；事件派发失败不能回滚已经提交的业务写入。
- 公开写入必须绑定授权、CSRF/Origin（适用时）、限流、幂等或重复指纹与统一错误语义；token 仅以摘要或受保护密文形式持久化。
- 任务 claim、租约、完成和失败状态更新必须是短事务；处理器不得在 claim 事务中执行网络、邮件、Markdown 重计算或媒体处理。
- 缓存、搜索、统计和媒体变体均可从权威数据重建。

## 数据保留

- 编辑快照：被下一次快照覆盖；内容永久删除时一并删除。
- 普通版本：最近 50 个；实际发布检查点不被普通上限淘汰。
- 回收站：默认 30 天。
- 会话：过期后分批清理。
- 任务成功记录：短期保留；失败记录保留至处理或达到配置期限。
- 统计日数据：12 个月后合并月度。
- 审计：默认一年，可导出并由站主清理。
- 备份：7 个日备份、4 个周备份。

永久清理始终分批执行，避免一个事务锁住数据库过久。
