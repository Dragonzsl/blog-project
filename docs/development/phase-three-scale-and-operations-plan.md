# 改进阶段三实施计划：规模与运维能力

> 上位路线图：[项目现状评估与改进路线图](../project-assessment-and-improvement-roadmap.md)
>
> 前置阶段：[改进阶段一实施计划](phase-one-production-hardening-plan.md)、[改进阶段一记录](../progress/phase-one-production-hardening.md)、[改进阶段二实施计划](phase-two-content-and-extension-contract-plan.md) 与 [阶段二记录](../progress/phase-two-content-and-extension-contract.md)
>
> 设计基线：2026-09-05
>
> 文档状态：实施前设计，不代表阶段三已经实施或验收完成

本文设计路线图中的“改进阶段三：规模和运维能力”。它与仓库中历史产品路线的 [stage-3](../progress/stage-3.md) 不同：后者记录的是导入器、只读 API、Webhook、发行物和浏览器回归等“生态与发布加固”能力，本计划不重复安排这些已存在的能力。

本阶段的目标是在不改变单站点、单 Owner、SQLite 模块化单体和默认关闭可选能力的前提下，让内容读取、公开渲染、后台任务、备份和恢复在较大数据量、并发缓存未命中和远程存储失败时仍保持有界、可观察、可恢复。

## 1. 阶段定位与目标

### 1.1 目标

阶段三围绕路线图的三项交付展开：

1. 内容、页面、媒体、分类/标签和搜索路径使用轻量投影、批量查询和有界分页，避免列表请求携带正文或逐项查询。
2. 页面缓存增加按 key 的请求合并，后台任务增加可操作的队列、租约、失败和耗时可见性。
3. 备份增加可选加密和远程存储适配，备份验证与隔离恢复演练形成自动化证据，并在管理端提供最小运维摘要。

### 1.2 阶段出口

阶段三完成必须同时满足以下条件：

- 固定测试数据达到约 10,000 条内容、包含大正文和多个媒体变体时，列表内存、查询次数、响应大小和并发度都有明确上限。
- 管理端和公开列表不读取 `body_markdown`；文章详情、编辑器和确实需要正文的渲染路径仍可读取完整正文。
- 分类、标签、封面和媒体变体使用批量读取；页面条目数增长不会导致等比例的 taxonomy 查询。
- 同一个公开缓存 key 在并发未命中时只产生一次渲染；渲染失败不会污染缓存，旧的 `render_epoch` 失效语义保持不变。
- 任务页能看到按状态和类型聚合的数量、最老任务、过期租约、重试次数、最近耗时和截断后的失败原因，并能安全重试失败任务。
- 本地备份仍默认可用；启用加密或远程目标后，创建、验证、断点失败和恢复演练均有可重复结果，密钥不进入数据库、日志或备份内容之外的公开记录。
- 管理端运维摘要能显示迁移、任务、备份新鲜度、验证/恢复演练、媒体存储和缓存状态，但不暴露绝对路径、访问凭据、密钥或完整隐私数据。
- ADR-0031 的现有预算继续通过：缓存命中 p95 不超过 25 ms、首次渲染 p95 不超过 150 ms、搜索 p95 不超过 250 ms、常规后台响应 p95 不超过 300 ms；新增大数据集门禁不通过时不能以提高资源预算掩盖回归。

## 2. 范围与非目标

### 2.1 纳入范围

- publishing 的管理列表、公开卡片、文章导航和详情查询投影。
- organization 的分类/标签批量富集、公开列表和管理选择器边界。
- media 的媒体列表、变体摘要、引用扫描和存储迁移的批量/可恢复处理。
- discovery 的搜索结果、归档、Feed、Sitemap 和索引同步的数量上限与查询形状。
- presentation 的公开页面缓存请求合并、缓存清理节流和可观测计数。
- operations 的任务快照、租约恢复、失败重试、备份加密、远程目标、验证和隔离恢复演练。
- admin 的任务和运维摘要页面，以及 CLI 的状态、备份和恢复输出。
- 固定数据集、查询次数、内存、恢复一致性、远程失败和浏览器/无 JavaScript 路径的验收测试。

### 2.2 明确不纳入

- 不引入 Redis、独立消息队列、独立搜索服务、PostgreSQL、微服务或全量 SPA。
- 不改变单站点、单 Owner、文章/页面两种内容类型和公开 permalink 规则。
- 不把缓存、搜索索引、统计、媒体变体或运维汇总变成权威数据。
- 不在本阶段扩展公开 Content API 的游标/增量协议；这属于路线图后续的公共能力阶段，内部查询先完成有界化。
- 不重新设计已有导入器、Webhook、内容 API、主题设置或插件 Host API；只补充它们使用轻量投影、任务状态和备份能力时需要的边界。
- 不将 `themes/cel-panel/`、`internal/presentation/cel_panel_theme_test.go` 或 `.playwright-results/` 纳入阶段三实现提交，也不把主题插件回归作为本阶段默认门禁。

## 3. 当前基线与缺口

阶段二已经完成封面和扩展契约闭合，阶段三应在现有接口上做小范围替换，不引入横向的通用 service/repository 层。

| 范围 | 当前基线 | 阶段三缺口 | 计划结果 |
| --- | --- | --- | --- |
| 管理内容列表 | `internal/publishing/repository.go` 的 `AdminContents` 使用完整 `contentSelect`，包含正文，并逐项调用 `enrichTaxonomy` | 大正文会放大读取和内存；taxonomy 查询次数随条目数增长 | 单独的管理列表投影、批量分类/标签、固定页大小/游标边界 |
| 公开文章列表 | `publishedContentsPage` 使用 `publicContentSelect`，列表卡片仍取正文；每项再调用 `enrichPublishedTaxonomy` | 首页、分类、标签和归档的卡片不需要正文，却承担详情字段成本 | `PublicCard` 投影只保留卡片字段，文章详情单独读取正文 |
| 分类/标签 | `PublicCategoryPage`、`PublicTagPage` 先取得内容 ID，再由上层读取内容；快照 taxonomy 的标签解析仍可能逐项查询 | 多层调用容易出现 N+1，Tag JSON 快照在大页上成本不可见 | 单页批量查询分类/标签，查询数随页面类型而非条目数线性增长 |
| 媒体 | `media.Repository.Items` 返回媒体及变体集合；`MigrateStorage` 先把所有对象装入内存再迁移 | 媒体库增长会造成无界切片、长任务不可恢复和进度不可见 | 媒体列表分页、变体数量/摘要上限、存储迁移按批次和游标处理 |
| 搜索/发现 | FTS5 结果已经是轻量字段，索引同步按 dirty 数量批处理；Feed、归档和 Sitemap 有 limit | 大页、重复 taxonomy 读取、同步/缓存耗时缺少统一证据 | 保持 FTS5 权威边界，补充结果上限、查询计划、同步进度和大数据集门禁 |
| 页面缓存 | `PageCache` 已有内存 LRU、磁盘缓存、epoch 失效和大小限制 | `serveCachedDocument` 的并发 miss 会重复渲染；每个 miss 都可能触发清理扫描 | 增加有界 render flight，清理改为节流/后台触发，预览不进入共享缓存 |
| 任务队列 | `jobs` 已有状态、租约、尝试次数、错误和幂等键；管理端已有基础任务页 | 缺少按 kind/年龄/租约/耗时的摘要；失败原因和运行指标不够可操作 | `TaskSnapshot`、尝试时间/耗时、过期租约和安全重试入口 |
| 备份 | `operations.BackupService` 生成本地 tar.gz，验证和恢复替换已有数据目录；`backups.encrypted` 当前记录未加密 | 没有加密/远程目标统一边界；恢复演练还需补充隔离启动与公开结果比较 | 可选 `BackupStore`、加密层、远程对象键、verify/drill 证据和保留策略 |
| 运维页面 | `ReadStatus` 已显示迁移、任务和最近备份时间；任务页面独立展示失败任务 | 缺少备份新鲜度、存储空间、缓存和媒体/迁移状态的统一视图 | 受限、脱敏、可缓存的管理端运维摘要 |

## 4. 不可改变的架构约束

- 数据库中的文章、页面、修订、媒体和设置仍是唯一权威；列表投影、搜索、缓存、统计和运维摘要必须可重建。
- 公开读取只能使用已发布修订；管理列表可以读取当前编辑状态，但不得把编辑快照或草稿投影到公开路径。
- 每个业务写入保持短 SQLite 事务；不得在事务中执行图片处理、归档压缩、加密、远程上传、网络请求或恢复解包。
- `internal/platform/database` 的单写连接、受限读池、WAL、外键、busy timeout 和 SQLite 编译标签不变。
- 所有批量操作必须有批次大小、总量/游标、上下文取消和失败恢复语义；禁止通过增大批次掩盖锁等待或内存增长。
- 外部备份存储和加密密钥是可选能力，默认仍使用本地、未加密备份；远程失败不得回滚已完成的内容发布。
- 密码、会话、TOTP、恢复码、API/Webhook/SMTP/S3 密钥、完整邮箱、原始访客地址和备份解密身份不进入日志、审计、任务错误或管理页面。
- 主题仍是受限 `html/template`，插件仍通过窄 Host 接口接入；本阶段不借机扩大插件权限或引入主题代码执行。
- 管理端保留无 JavaScript 路径、键盘焦点、可见焦点环、ARIA 状态和窄屏布局；运维摘要不依赖前端轮询才能工作。
- 继续遵守约 0.85 CPU/256 MiB 容器和默认 192 MiB Go heap soft limit；所有新缓存、队列、并发和临时文件均有上限。

## 5. 目标设计

### 5.1 轻量读取投影

不要让 `Article` 同时承担详情实体和列表 DTO。实现阶段应为各读取场景定义小而稳定的内部投影：

- `AdminContentRow`：公开 ID、kind、状态、slug、标题、摘要、封面摘要、发布时间/更新时间、字数/阅读时间、分类和标签摘要；不包含 `body_markdown`、完整修订和编辑快照。
- `PublicCard`：标题、摘要、永久链接、发布时间、阅读时间、分类/标签摘要、公开媒体视图；不包含正文和编辑字段。
- `MediaListRow`：公开 ID、原始名、MIME、大小、尺寸、替代文字、创建时间和有限变体摘要；不读取对象内容。
- `TaskSnapshot` 与 `OperationsSummary`：只包含聚合数量、年龄、状态、截断错误和安全的相对/分类信息，不包含 payload 原文或绝对路径。

查询层采用“列表选择器”和“详情选择器”分离：

1. `contentListSelect` 只选择列表字段；`contentDetailSelect` 保留完整 Markdown，供编辑、预览和文章详情使用。
2. `publicCardSelect` 只连接已发布修订的标题、摘要、封面公共 ID 和 taxonomy 快照；公开文章详情才使用正文。
3. `scanContentList`、`scanPublicCard` 和 `scanContentDetail` 各自校验列顺序，避免为了复用扫描函数重新把正文带回列表。
4. 旧的 `Articles`、`Pages` 等兼容服务方法先保留，但内部调用改为明确的详情或列表投影；不通过 `body_markdown` 为空来伪装轻量查询。

这样可以把“是否需要正文”变成代码层面的契约，而不是依赖调用方记住不要使用某个字段。

### 5.2 批量 taxonomy 与媒体读取

每个列表页先得到最多一页的 content 公共 ID，再进行批量富集：

- 分类使用单条 `IN`/临时值表查询；标签使用一条 `json_each` 聚合查询，按 content 公共 ID 分组。
- 当 SQLite 参数或结果上限不适合单条查询时，按 64 或 128 个 ID 分块；块大小是常量并纳入测试，不允许由请求参数放大。
- 缺失的历史分类/标签只返回空摘要并记录可去重的内部诊断，不阻断公开列表。
- 媒体列表按页加载；变体只返回配置允许的最多 4 个摘要。公开卡片通过封面公共 ID 批量解析媒体安全视图，无法解析时回退无图。
- 删除保护继续扫描正文和封面引用；性能优化不能以跳过引用扫描换取速度。

`discovery` 的 FTS5 正文索引同步仍可以读取已发布正文，因为那是后台索引任务且已有批次边界；它不能把正文带进搜索结果或列表投影。索引重建、媒体变体生成和存储迁移均不得在 HTTP 请求中同步完成。

### 5.3 有界分页策略

- 管理内容、媒体、任务、备份和存储迁移使用固定上限的页大小；能自然按 `(updated_at, id)` 或 `(created_at, id)` 排序的列表优先采用 keyset cursor，避免深页 `OFFSET` 扫描。
- 公开文章、归档、分类和标签保留现有页码 URL，以免破坏 SEO 和用户书签；每页大小仍由服务端钳制。若需要限制过深页，必须返回稳定的 404/400 语义并在模板和文档中说明，不静默返回错误数据。
- 阶段三不改变 Content API 的公开分页协议；公开 API 的 cursor/`updated_since` 作为后续公共能力阶段单独设计兼容策略。
- 所有 `limit`、`per_page`、cursor token 和批处理参数都在服务层再次校验，不能只依赖模板或 HTTP handler。
- 总数统计与列表查询分离；无法低成本精确统计的内部运维列表可以显示“超过上限”，但公开分页的 `total` 语义不能被悄悄改变。

### 5.4 页面缓存请求合并

在 `PageCache` 之外增加小型 `RenderFlight`，只服务公开、可缓存的页面：

- key 至少包含 `render_epoch`、语义 key、内容类型和当前主题资源身份；preview、admin、带用户状态的响应永不进入共享 flight。
- 同一 key 只有一个 leader 执行渲染，其余请求等待结果并复制响应字节；flight map 有固定最大条目数（建议 32），超出时直接执行一次有界渲染而不扩大 map。
- leader 使用脱离客户端取消但有明确 deadline 的渲染上下文，避免第一个断开连接的客户端取消所有等待者；超时或错误只唤醒等待者，不写入 PageCache。
- 成功结果仍通过现有 `PageCache.Put` 写入，epoch 失效、ETag、Last-Modified 和 `X-Page-Cache` 语义保持一致。
- 磁盘 `Prune` 不在每个 miss 上完整扫描；改为启动时一次、成功写入达到阈值后一次或生命周期 ticker 定期执行，并保持每轮最多删除固定数量。
- 共享等待、渲染失败、缓存写入失败和 flight 超限都写结构化计数，不记录正文或请求隐私。

### 5.5 后台任务可观测性

统一队列继续以 `jobs` 为权威，不新增独立队列服务。阶段三补足两层信息：

1. 数据库层在任务行记录最近一次开始、完成和耗时，保留现有租约、尝试次数、可执行时间、幂等键和截断错误。
2. `TaskQueue.Snapshot` 在单次短读事务中返回总数、按 kind 聚合、最老 pending、最老 failed、已过期 running 租约、最近失败时间和最近耗时；不解码或返回任务 payload。

任务处理规则：

- claim、lease、complete、fail 和 reopen 都是短事务；过期 running 任务只能按幂等键重新进入 pending，不能复制一行绕过唯一约束。
- 处理器错误通过统一脱敏/截断函数写入 `last_error`，网络响应、凭据、正文和 token 不得进入错误摘要。
- 永久失败仍保留 failed 状态；管理端“重试”复用原任务行、增加审计并重新计算 `available_at`，不能无限重试或绕过失败原因。
- in-process 计时器可以提供最近一分钟的请求/任务计数，但数据库状态和审计仍是重启后的权威；内存计数器必须有固定标签数和容量。

### 5.6 备份加密与远程存储

备份使用独立于媒体存储的 `BackupStore` 能力边界，避免把媒体对象生命周期和备份保留策略耦合。建议接口方向如下：

    type BackupStore interface {
        Name() string
        Put(context.Context, string, io.Reader, int64, string) error
        Open(context.Context, string) (io.ReadCloser, error)
        Stat(context.Context, string) (BackupObject, error)
    }

实现顺序：

1. `local` store 继续写入受保护的本地备份目录。
2. `s3`/S3-compatible store 复用已有受限 HTTP/S3 配置和超时策略，但在 operations 中使用独立的 backup prefix；不默认把备份上传到媒体 prefix。
3. 加密作为 archive writer/reader 外层适配器，第一版支持 `none` 和 age 类公钥加密；密钥通过受限文件或外部 secret 引用注入，数据库只保存 `encrypted` 和非敏感 key reference，不保存私钥/解密身份。
4. 创建流程先在本地 staging 目录生成并校验内容清单，再流式压缩/加密，计算最终对象 checksum，最后在事务外上传远程对象。远程对象使用不可猜测的公共 ID 和版本化 key，不覆盖已有对象。
5. 上传成功后才记录或更新 `backups` 权威记录；网络失败保留可清理的 staging 状态和失败摘要，不把未完成对象标记为 valid。

备份 manifest 必须包含应用版本、迁移版本、站点公共身份、格式版本、加密状态、文件数量、大小和每个条目的 checksum。manifest 中不能包含密码、session、TOTP、恢复码、密钥原文或完整访客隐私数据。

### 5.7 验证与恢复演练

- `backup verify` 从 local 或 remote store 读取对象，必要时解密到受限临时目录，验证外层 checksum、manifest、条目路径、大小、哈希和数据库迁移版本。
- `backup drill` 必须使用隔离目标目录和独立数据锁；不得停止/覆盖当前实例的数据目录。演练流程为：读取备份 → 验证 → 恢复到临时目录 → 以临时端口启动新实例或执行等价健康检查 → 检查 `/livez`、`/readyz`、首页、登录保护、一篇带媒体文章、搜索和 RSS/API 的确定性摘要 → 清理临时目录。
- 恢复失败保留当前站点不变，并留下失败审计；成功演练更新 `restore_tested_at` 和可验证的摘要，不把临时实例的绝对路径写入页面或日志。
- 真正的 `restore --replace` 仍先保留 rollback 目录；解密、解包、数据库审计和路径校验全部完成后才交换目录。

### 5.8 Web 运维摘要

在现有后台任务页上增加独立的 `OperationsSummary`，数据来源限定为有界聚合查询和受限文件系统统计：

- 数据库迁移版本、ready 状态和最近一次启动/重建结果。
- pending/running/failed 任务、过期租约、最老失败年龄、最近失败类型和可安全重试数量。
- 最近有效备份的创建时间、年龄、目标类型、是否加密、验证时间和最近恢复演练时间。
- 媒体对象/变体数量、存储迁移状态、已处理/总数和最近错误摘要。
- 页面缓存条目数、近似磁盘大小、当前 epoch 和最近一次清理结果。
- 仅显示健康状态和相对年龄；绝对 data path、S3 endpoint 中的敏感部分、object key 以外的凭据和错误 payload 不输出。

摘要可以在进程内以 15 秒左右的最小刷新间隔缓存，避免每次打开后台都扫描备份目录和媒体目录。没有 JavaScript 时仍渲染上一次安全快照和“更新时间”，管理员可通过普通刷新获得新数据。

## 6. 实施切片与依赖

### 6.1 S0：规模基线与契约冻结

**目标**：在改代码前获得可比较的查询、内存、缓存和恢复基线。

- 建立 1,000 / 10,000 条内容、长正文、分类/标签、媒体和变体的可重复 fixture 生成器。
- 记录管理列表、首页、分类、标签、归档、搜索、媒体列表、任务页和备份状态的 SQL 数量、返回字节和峰值内存。
- 用 `EXPLAIN QUERY PLAN` 检查现有查询；只有被真实查询证明的索引才能进入迁移设计。
- 冻结分页上限、批大小、flight 容量、备份并发和远程响应/文件大小限制。
- 输出阶段三的查询投影接口、迁移草案、性能基线和失败样本。

**依赖**：阶段二代码、ADR-0031、固定 Go 构建标签。

### 6.2 S1：Publishing 与 Discovery 轻量投影

**目标**：先解决最大且最容易验证的正文加载与列表 N+1。

- 增加 publishing 的 list/card/detail 选择器和扫描器。
- 将后台内容列表、回收站摘要、首页/文章列表、分类/标签、归档和相关文章切换到轻量投影。
- 为 taxonomy 增加批量查询 API；保留文章详情和编辑器的完整 `Article` 路径。
- 为列表补充页大小、深页和空结果边界测试，并验证草稿不会进入公开投影。

**依赖**：S0 投影字段与查询计划。

### 6.3 S2：媒体、分类标签和搜索批处理

**目标**：消除媒体列表/变体与 taxonomy 的无界加载。

- 增加 `MediaListPage` 和变体摘要边界；下载媒体正文仍走单项 Asset 路径。
- 将存储迁移改为按 `(media_id, variant_key)` 游标分批，记录进度，支持中断后继续，单批只保留一个临时对象。
- 搜索/归档/Feed/Sitemap 使用服务层 limit；同步 dirty 文档按固定批次并暴露进度，不在结果中携带正文。
- 对分类/标签和封面媒体解析建立上限明确的批量测试和缺失数据回退测试。

**依赖**：S1 的列表投影；必要时先落地 `00016` 的迁移进度字段。

### 6.4 S3：缓存请求合并与任务观测

**目标**：降低并发冷缓存的重复渲染，并让持久任务可诊断。

- 实现有界 `RenderFlight`，接入 `serveCachedDocument`，保留预览隔离、ETag 和 epoch 失效。
- 将 PageCache 清理从请求热路径移到阈值/ticker 触发，并增加缓存命中、miss、coalesced wait 和清理计数。
- 扩展 `jobs` 的最近尝试时间/耗时字段和 `TaskQueue.Snapshot`，增加过期租约恢复与安全 reopen 测试。
- 更新任务页面和 CLI status，确保失败摘要已脱敏且不显示 payload。

**依赖**：S0 的并发和预算；不依赖远程备份。

### 6.5 S4：备份目标、加密和恢复演练

**目标**：形成“可验证、可远程保存、可隔离恢复”的运维闭环。

- 冻结 `[operations.backup]` 配置、secret 引用和默认行为；配置错误不能影响普通站点启动之外的核心公开读写。
- 实现 local `BackupStore` 适配和 S3-compatible 适配；所有网络请求设置 timeout、响应大小限制和私网/重定向策略。
- 实现加密 writer/reader、远程临时对象清理、checksum/manifest 更新和失败重试边界。
- 扩展 CLI：`backup create --destination`、`verify`、`drill`、`list` 输出目标、加密、校验和演练时间，但不输出密钥。
- 用 fake store、临时密钥和临时端口覆盖上传中断、远程 5xx、错误密钥、损坏对象、重复启动和恢复后公开结果对比。

**依赖**：S0 的预算和恢复 fixture；S3 只依赖 operations，不改变媒体 adapter 的默认行为。

### 6.6 S5：管理端运维摘要与发布门禁

**目标**：把阶段三的状态聚合成站内可读、低成本和可操作的页面。

- 增加运维摘要 view model、脱敏转换和有界刷新缓存。
- 在后台 dashboard/operations 页面展示任务、备份、存储迁移、媒体和缓存状态；危险操作继续要求 CSRF/Origin 和二次确认。
- 添加无 JavaScript、窄屏、ARIA、错误摘要和空状态测试。
- 运行全量测试、race、vet、性能门禁、大数据集 benchmark、浏览器回归、SBOM/license 和隔离恢复演练。
- 更新架构、数据模型、运维命令、配置示例和阶段进度模板；本阶段结束前再记录实际结果，不能提前把本计划改写成完成记录。

**依赖**：S1–S4 的稳定 view model 和运维查询。

依赖顺序：`S0 → S1 → S2`；`S0 → S3`；`S0 → S4`；`S1–S4 → S5`。

## 7. 数据库与迁移计划

阶段三第一版建议使用下一编号迁移 `db/migrations/00016_phase3_scale_operations.sql`。实际字段必须在 S0 通过真实查询确认后再落地，不能为了计划预建宽索引。

### 7.1 任务可观测性

候选字段：

- `jobs.last_started_at`、`jobs.last_completed_at`、`jobs.last_duration_ms`，均允许为空且非负。
- `jobs_observability_idx(status, updated_at DESC, id)`，仅在任务摘要和失败列表查询需要时创建。

不在 `jobs` 中保存完整 attempt history 或 payload 副本；若以后需要长期统计，先设计有界保留表，不能让每次重试无界增长。

### 7.2 存储迁移进度

候选字段：

- `storage_migrations.cursor_media_id`、`cursor_variant_key`、`batch_size`、`updated_at`、`lease_expires_at`。
- 按状态和更新时间建立最小索引，支持恢复/巡检，不为每个对象建立额外历史表。

每次批次完成才推进游标；对象校验失败保持迁移记录为 failed，重试从最后一个未确认对象继续，不提前改写 `media_storage_locations`。

### 7.3 备份元数据

现有 `backups.destination`、`encrypted`、`checksum_status` 和 `restore_tested_at` 继续保留。若 S0 证明 `manifest_path` 同时承担本地路径和远程对象键会造成歧义，再增加 `object_key`、`verified_at` 和有限 `last_error`；远程 secret、解密身份和响应正文不落库。

不要为缓存、公开列表投影或搜索结果增加权威表。代码级投影和可重建的 FTS5 仍足够支撑阶段三。

### 7.4 空库、升级和失败重启

- 空库直接运行全部迁移，检查严格表、索引和默认值。
- 从迁移版本 15 升级，确认旧任务、旧备份和未完成 storage migration 可读取。
- 迁移执行中断后再次启动必须幂等；不使用 Goose Down 作为生产回滚。
- 任何新增远程备份配置都默认不启用，旧配置不因未知字段或缺少 secret 失效。

## 8. 测试与验收矩阵

| 领域 | 必须覆盖 | 证据 |
| --- | --- | --- |
| 列表投影 | 大正文列表不读取 `body_markdown`；详情仍完整；草稿/回收站隔离 | repository/service 单测、SQL 形状断言、公开 HTTP 测试 |
| taxonomy 批量 | 100 条卡片的分类/标签查询数量有固定上限；缺失标签不阻断页面 | fake DB/query counter、真实 SQLite fixture |
| 分页边界 | `limit`、深页、空页、cursor、并发读取和总数语义稳定 | publishing/discovery/media/operations 测试 |
| 媒体迁移 | 单批内存有界；中断后继续；源/目标 checksum 不同即失败；重复运行不破坏源 | `media` storage migration 测试、临时 fake store |
| 搜索与发现 | 10,000 条内容下搜索结果不带正文；dirty 同步固定批次；Feed/Sitemap 上限有效 | `internal/perf` benchmark 与 discovery tests |
| 缓存合并 | 50 个并发 miss 只有一次 render；不同 epoch 不合并；错误不入缓存；flight 达上限可恢复 | presentation concurrency tests、race |
| 任务观测 | 过期租约恢复、失败截断、人工重试幂等、重启后状态可见、按 kind 聚合 | operations/extensions tests、CLI 输出断言 |
| 备份加密 | 错误 key、截断文件、篡改 manifest、远程 5xx、重复对象、上传取消均安全失败 | backup fake store、临时 secret、审计断言 |
| 恢复演练 | 隔离目录启动、健康检查、首页/带媒体文章/搜索/RSS 或 API 对比；现有目录不变 | `backup drill` 集成脚本和恢复报告 |
| 运维页面 | 无 JS、窄屏、ARIA、空状态、敏感字段不出现、摘要查询有上限 | `web/admin` tests、browser regression |
| 资源预算 | 10,000 条内容、大正文和多个变体下内存/CPU/DB 连接不持续增长 | `make perf-gate`、新增规模 benchmark、容器运行记录 |

## 9. 工程验证门禁

代码与文档门禁：

- `make build`
- `make test`
- `make test-race`
- `make vet`
- `go mod verify`
- `git diff --check`

阶段三新增或加强：

- `make perf-gate`
- `make stage3-acceptance`
- `make sbom`
- `make license-audit`
- `BROWSER_STRICT=1 make browser`

备份/恢复必须额外执行：

- `blog backup create`
- `blog backup verify --archive <verified-object>`
- `blog backup drill --archive <verified-object>`
- `blog restore --target-data-dir <isolated-dir>`

如果 Go、Docker、Playwright、浏览器、SBOM 或许可证工具不可用，必须在阶段进度记录中标记具体未运行项；不能把单元测试或本地 HTTP 请求表述成 Compose、三浏览器或恢复演练证据。主题插件专用回归仍不属于本阶段默认门禁。

## 10. 失败处理、回滚和安全边界

### 10.1 查询投影回滚

- 新投影与旧详情查询并行保留一个切片；发现字段不一致时关闭列表投影开关或回退到旧查询，不修改权威数据。
- 公开视图优先返回无封面/空 taxonomy，而不是读草稿或猜测旧数据。
- 新索引只优化真实查询；删除索引不会改变数据语义，迁移失败不能阻塞已有内容读取。

### 10.2 缓存和任务回滚

- Render flight 只影响重复计算，不改变缓存权威和响应内容；flight panic、超时或 map 满时直接回退单次渲染。
- 任务观测字段写入失败不应丢失业务任务；完成/失败状态优先保证幂等，观测字段可降级为空并记录内部错误。
- 任务处理器版本不兼容进入可见 failed/blocked 状态，禁止忙循环和无界重试。

### 10.3 备份和远程存储回滚

- 加密或远程配置错误时，普通站点启动应明确报告备份能力不可用，但不能删除或覆盖上一次有效本地备份。
- 远程上传只有在 checksum 和 manifest 验证后才登记 valid；失败对象使用唯一 key，清理失败也不能复用该 key。
- 恢复始终先验证、再 staging、再审计数据库、最后交换目录；任何一步失败都保留当前数据和 rollback 副本。
- 加密密钥轮换只影响新备份；旧备份的 key reference 必须可识别，不能静默用新密钥解密旧对象。

## 11. 推荐 Git 分批方式

本计划本身应作为单独文档提交；阶段三实施时继续遵守“除主题插件外，按模块分批提交”：

1. `docs(phase3): add scale and operations implementation plan`
2. `feat(database): add phase-three observability and migration cursors`
3. `feat(publishing): add bounded list projections and taxonomy batching`
4. `feat(discovery): bound search and public discovery queries`
5. `feat(media): make storage listings and migrations resumable`
6. `feat(presentation): coalesce public cache renders`
7. `feat(operations): add task observability and backup stores`
8. `feat(operations): add backup encryption and isolated drill`
9. `feat(admin): add operational summary`
10. `test(phase3): add scale, recovery and resource gates`
11. `docs(phase3): record actual acceptance and residual risks`

每个代码批次都应包含自己的失败路径测试；数据库迁移和实现可以拆分，但不能让中间提交把 `LatestMigrationVersion`、schema 或启动路径置于不一致状态。`themes/cel-panel/`、其专用测试和运行产物继续保持未提交、独立边界。

## 12. 实施前决策清单

开始 S1 前必须冻结以下选择，并在需要时单独新增 ADR：

- 管理列表和媒体列表的 cursor 编码与过期语义；公开页码 URL 是否设置深页上限。
- `jobs` 观测字段是直接扩展任务行，还是采用有界 attempt summary 表。
- 备份远程目标是否复用现有 `storage.s3` 凭据结构，还是使用独立 `[operations.backup]` 配置；本计划建议独立命名空间。
- 加密第一版使用 age recipient/identity 文件还是站外 KMS 适配；本计划建议先提供可替换接口和 age 类文件方案，不在 SQLite 保存密钥。
- 恢复演练是否允许启动临时 Go 进程，或在受限环境中使用应用内 health/render 检查；发布环境应优先使用真实临时进程。
- 运维摘要的刷新间隔、缓存失效和磁盘使用统计在不同平台上的实现；统计失败时显示未知，不猜测为健康。

## 13. 阶段三完成条件模板

实施结束后，另建或补充 `docs/progress/phase-three-scale-and-operations.md`，至少记录：

- 实际 Git 提交和未纳入的主题/运行文件。
- 10,000 条内容及大正文 fixture 的查询、内存、延迟和连接数据。
- 缓存并发合并、任务重启/租约、远程失败、加密校验和恢复演练日志。
- `make test`、race、vet、perf-gate、stage3-acceptance、browser、SBOM/license 的实际结果与未运行原因。
- 当前配置、迁移版本、备份目标/加密状态和任何待补验风险。

在这些证据生成前，不应把阶段三标记为完成，也不应把 `docs/progress/stage-3.md` 的历史验收记录复制为本阶段证据。
