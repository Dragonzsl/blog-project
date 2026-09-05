# 改进阶段二实施计划：内容与扩展契约闭合

> 上位路线图：[项目现状评估与改进路线图](../project-assessment-and-improvement-roadmap.md)
>
> 前置阶段：[改进阶段一实施计划](phase-one-production-hardening-plan.md) 与 [阶段一完成记录](../progress/phase-one-production-hardening.md)
>
> 计划基线：2026-09-04
>
> 文档状态：阶段二实施完成记录（2026-09-04）

本文是改进路线图中“阶段二：内容与扩展契约闭合”的实施计划与实际完成记录。它不是历史开发记录中的“阶段二”（例如 [stage-2](../progress/stage-2.md)）。

阶段二的内容、主题契约和插件隔离实现已经完成，并通过固定构建标签下的代码门禁及本地服务 HTTP/CLI 操作验收。当前工作树中用户既有的未提交主题开发文件继续保留，不纳入其他模块的提交边界；浏览器三引擎和 Docker Compose 运行态检查因本机缺少 Playwright/浏览器且无 Docker daemon 权限，作为未运行项单独登记。

## 1. 阶段定位与目标

阶段一已经建立了生产安全与一致性的基础：可信代理客户端身份、公开写入保护、事务内事件/任务记录、有限重试和最小任务可见性已经进入主线。阶段二在这些基础上，补齐已经存在但尚未贯通的内容和扩展契约。

阶段二结束时，系统应达到以下状态：

1. 媒体封面从管理端选择开始，贯通草稿、不可变修订、发布、公开查询、主题渲染、归档和导入；删除保护与正文引用使用一致的媒体引用语义。
2. 主题设置成为有版本、有校验、可安全注入模板的只读视图；安装、样例渲染、校验和复核、激活、回退和重启恢复形成闭环。
3. 插件路由、设置、事件和任务具有明确的宿主边界、版本和幂等规则；插件初始化失败、禁用、重新启用和进程重启不会破坏宿主或插件数据。
4. 新增能力不改变单站点、单 Owner、文章/页面两种内容类型、Markdown 权威正文、服务端渲染和 SQLite 单写连接的总体架构。

阶段出口不是“页面看起来能显示”，而是每条契约同时有实现、失败路径、重启/恢复证据、隔离测试和文档。

## 2. 范围与非目标

### 2.1 纳入范围

- 媒体封面选择、校验、引用关系和删除保护。
- 封面在编辑快照、修订、发布、取消发布、恢复、垃圾箱恢复中的语义。
- 公开内容、首页卡片、文章页、主题视图模型、只读内容 API 和归档导入导出的封面一致性。
- 主题 manifest、设置 schema、设置管理页、固定 fixture 渲染矩阵、包内容校验、运行时激活和回退。
- 主题数据库状态与运行时指针的重启一致性。
- 插件公开扩展槽、插件命名空间、路由冲突、菜单路径和初始化回滚。
- 插件设置版本、类型约束、敏感值处理和配置迁移。
- 插件事件订阅、任务注册、载荷上限、版本解码、幂等和禁用/重启行为。
- 阶段二相关的浏览器回归、资源预算和数据库迁移验收。

### 2.2 明确不纳入

- 多用户、角色权限、读者账户、会员和多租户。
- 全量 SPA、全局客户端状态、新前端框架、Redis、独立消息队列或独立搜索服务。
- 在线主题/插件市场、远程下载、任意脚本执行或主题服务端代码执行。
- 阶段三的完整查询瘦身、taxonomy N+1 专项、搜索规模专项和运维中心。
- 备份加密、异地备份、Owner/站点设置、修订 diff、批量编辑和排程日历。
- 新增评论、Newsletter、Webhook 产品功能；本阶段只验证它们能遵守收紧后的插件契约。

## 3. 当前基线与缺口

| 领域 | 当前已有能力 | 阶段二缺口 | 影响 |
| --- | --- | --- | --- |
| 媒体 | 数据库已经有 contents.cover_media_id；media_references 已支持 body/cover；媒体具备原图和响应式变体 | publishing.Article、DraftInput、Revision、查询、编辑器、归档/importer 尚未完整携带封面；当前引用重建只处理正文 | 后台保存、公开显示、回滚和删除保护可能各自表达不同事实 |
| 公开视图 | presentation.ContentView 已预留 Cover 和 MediaData；文档也定义了 MediaView | 内容查询还没有把发布修订的封面解析成公开媒体视图；卡片、API、Feed/归档路径需逐一确认 | 主题可能拿不到封面或错误读取草稿数据 |
| 主题安装 | ZIP 路径/大小/文件数、manifest、模板解析和嵌入式 fallback 已有基础校验 | 设置未进入渲染；缺少代表性 fixture 矩阵；安装 checksum 与目录内容复核、DB active 状态和 active.json 之间存在故障窗口；Rollback 可能只回到嵌入式 fallback | 坏主题可能在边界故障下接管站点，设置变化也无法形成稳定契约 |
| 插件路由 | 管理路由会自动落在 /admin/plugins/{id} 下；处理器有启用守卫；评论 provider 有互斥检查 | 公开路由允许过宽；没有统一的宿主路由槽/命名空间与冲突注册表；初始化中途失败时已注册项不能完整回滚 | 插件可能覆盖宿主或其他插件路由，重复启用也可能重复注册 |
| 插件设置 | 已有类型、默认值、数值范围和数据库 schema_version | manifest 与存储版本未形成迁移契约；没有敏感字段/脱敏读取和大小边界的统一规则 | 升级、禁用重启和配置回滚时容易丢值或泄露秘密 |
| 事件任务 | 阶段一已有 durable event、任务租约、退避、幂等和失败重试入口 | 插件可注册的事件/任务定义、版本解码、载荷上限和重复注册隔离还不够严格 | 旧任务、坏载荷和插件重新启用可能变成不可观察的失败 |
| 归档/importer | 内容正文、基础元数据、离线导入和安全 ZIP 校验已存在 | manifest 没有封面/媒体引用语义；导入模型也没有封面字段 | 数据迁移会丢失封面，且无法验证引用完整性 |

当前主题开发目录、主题测试和 presentation 修改是未提交的开发输入，不作为本计划的完成证据。主题阶段必须用安装包、数据库记录、重启和浏览器验收重新证明。

### 阶段一运行基线注意

阶段一的代码提交和 Go 测试状态已作为本计划前置条件。此前观察到的 purge public-write safety records SQLite `near LIMIT` 语法错误已在 `internal/platform/publicwrite/guard.go` 中改为有界子查询删除；阶段二服务启动日志已重新取得，未再出现该错误。

## 4. 不可改变的架构约束

阶段二所有实现都必须继续遵守以下约束：

- 一个部署只有一个 Site 和一个 Owner；不引入通用用户、角色、成员或租户。
- Markdown 正文、不可变 content_revisions 和稳定 permalink 是权威数据；缓存、搜索索引、媒体变体和公开视图都必须可重建。
- 公开读取只能使用已发布修订，不能从编辑快照或草稿读取封面、正文、标题或分类标签。
- 业务写入使用一个短 SQLite 写事务；模板解析、Markdown 重处理、图片处理、网络、邮件和 Webhook 不得放进事务。
- 变更公开渲染的写入必须递增 system_state.render_epoch。
- 主题只能使用受限 Go html/template 和静态资源；模板不读取环境、数据库、文件系统或插件私有配置。
- 插件只能通过 extensions.Host 的窄能力接口工作；不能获得裸 sql.DB、通用文件系统或进程执行器。
- 插件默认关闭；禁用只停止入口、事件和任务消费，不删除配置、数据、投递记录或历史任务。
- 新增外部网络访问必须有显式适配器、超时、私网拒绝、失败分类和可重试语义。
- 所有列表、导入、ZIP、模板输出、媒体变体和后台任务都必须有明确上限。

## 5. 目标契约设计

### 5.1 封面是媒体引用，不是任意 URL

封面必须引用已上传的媒体项，不能由表单直接写入任意 URL。封面候选默认只接受安全解码后的图片媒体；SVG 继续按现有安全策略拒绝。封面媒体可以使用原图和已经生成的响应式变体，缺失某个变体时回退到原图，不阻断文章正文。

建议采用以下分层：

| 层 | 权威字段/对象 | 语义 |
| --- | --- | --- |
| 当前编辑状态 | contents.cover_media_id | 当前草稿/编辑状态选中的媒体，只有 publishing 事务可以修改 |
| 不可变修订 | content_revisions.cover_media_public_id 与 cover_snapshot_version | 记录该修订当时的封面；空值表示该修订明确没有封面；旧历史行用版本 0 表示“迁移前未记录” |
| 引用保护 | media_references(relation=cover) | 维护当前状态和已发布状态仍可能使用的封面集合；与正文引用一起参与删除检查 |
| 管理模型 | publishing.Article 的当前封面引用 | 供编辑器、预览和管理列表使用；可以携带内部服务所需的引用，但不能直接透传到公开模板 |
| 公开模型 | presentation.MediaData / MediaView | 只包含稳定媒体 URL、替代文本、尺寸和安全的 srcset；不得包含内部 SQLite 行 ID、存储路径或私有对象 key |

需要先在迁移前执行一次非空审计。由于旧 content_revisions 没有历史封面字段，不能把当前 contents.cover_media_id 自动假设为所有历史修订的封面。迁移策略如下：

1. 旧修订默认标记为 cover_snapshot_version=0，不伪造历史事实。
2. 能明确对应当前修订的记录才回填为版本 1；当前封面为空时，版本 1 的空值表示明确无封面。
3. 当前修订与已发布修订不同时，不把草稿封面回填为已发布封面；已有非空数据必须先通过一次性校验/人工映射处理。
4. 恢复版本 0 的历史修订时不静默清除现有封面；服务返回“该历史版本没有封面快照”的可识别状态，编辑器保留当前封面并要求确认。
5. 新保存、发布、恢复和导入创建的修订必须使用版本 1，封面存在性在同一写事务内验证。

引用重建应收敛为一个事务内操作：同时重建正文引用和封面引用，并将当前修订与已发布修订需要保护的媒体取并集。不能只更新 contents.cover_media_id 而遗漏 media_references，也不能只插入 cover 引用而不更新修订快照。

### 5.2 公开封面只能来自已发布修订

公开内容查询必须从 published_revision_id 对应的封面快照解析媒体，而不是读取当前草稿的 contents.cover_media_id。管理端详情和编辑器读取当前修订；预览根据明确的预览修订读取。

公开输出规则：

- 文章页、文章列表卡片、首页精选/最近列表、目录和搜索结果统一使用同一个安全媒体 view builder。
- 找不到媒体或变体损坏时，公开页面继续渲染正文并按“无封面”降级，同时记录受限错误/审计信息；不把数据库路径或异常详情输出给访客。
- API、Feed、Sitemap、归档 manifest 和主题模板不能泄露内部媒体 ID、object key、磁盘路径或私有存储配置。
- 封面替代文本来自媒体安全字段；如果为空，模板必须仍然生成可访问的图片语义或使用无图布局，不能把标题字符串拼接成未转义 HTML。

### 5.3 主题设置是有版本的只读视图

主题 manifest 继续只声明展示设置，不声明代码能力。建议在现有 SettingsSchema 基础上固定：

- manifest 增加明确的 settingsVersion；数据库 theme_settings.schema_version 与该版本一致。
- 每个字段必须声明类型、默认值、长度/数值范围或选项；未知类型、重复选项、超长 key 和过大默认值在安装时拒绝。
- media 设置只保存稳定媒体公共 ID，渲染时由核心解析为 MediaView；模板不自行查询媒体。
- URL 设置默认限制为站内或显式允许的安全范围；不因主题设置而默认引入第三方字体、脚本、图片、分析或 CDN。
- 敏感值不允许进入主题模板。如果未来确需敏感配置，必须标记为 secret、只保存摘要/引用，并在模板上下文中完全隐藏；主题不是秘密管理器。
- 配置更新使用受保护的服务端表单、CSRF、Origin 和输入上限；成功更新后递增 render_epoch，使旧缓存失效。
- 模板收到 ThemeSettingsView，只读、已校验、只包含 schema 中允许的键；不把原始 JSON、数据库连接或插件设置传给模板。

### 5.4 主题包必须通过代表性渲染后才能激活

安装校验分为三层：

1. 包安全：文件数量、压缩后总大小、单文件大小、相对路径、常规文件、无符号链接和无特殊文件。
2. 契约安全：manifest、主题 API、核心版本范围、必需模板、模板函数、静态资源、设置 schema 和内容安全策略。
3. 运行安全：使用固定 fixture 渲染所有公开页面和状态，检查错误、输出大小、模板缺失、公开字段泄露和无 JavaScript 基础路径。

fixture 至少包含：

- 空首页、单篇文章、多篇列表、分类/标签目录和归档目录；
- 中文/英文混排、超长标题、长摘要、代码块、空正文和特殊字符；
- 无封面、有原图封面、有响应式变体、缺失变体和媒体不存在；
- 预览、404/状态页、搜索结果、分页和导航抽屉；
- 评论/Newsletter 功能开关开启和关闭；
- 已有主题的自定义 directory.html/status.html，确认 fallback 不覆盖自定义模板。

渲染 fixture 不发网络请求、不读真实站点秘密、不写生产数据库；每个 case 都有最大输出大小和超时。

### 5.5 主题激活以数据库为权威，运行时标记为可修复缓存

现有 themes.active、active.json 和内存 ThemeManager 需要明确角色：

- themes.active 是跨进程、跨重启的激活权威状态。
- active.json 是运行时启动加速和故障恢复用的原子缓存，不得单独决定生产主题。
- 没有可用的有效主题记录时，嵌入式默认主题是安全 fallback。
- 主题激活不能出现数据库有多个 active 记录，也不能在普通回退后把所有自定义主题状态误清空。

建议流程：

1. 将上传包置于私有 staging 目录，完成包校验、目录内容 checksum 和 fixture 渲染。
2. 重新计算已安装目录的确定性内容 checksum；发现目录被外部修改、manifest 不匹配或 checksum 不一致时，拒绝激活。
3. 读取当前 active 记录，保存 previous theme 作为回退目标。
4. 在短事务中更新 active 记录和激活审计；事务内不解析模板、不读大文件、不做网络请求。
5. 提交后原子写入 active.json，再切换内存主题；active.json 写入失败时由启动 reconcile 根据数据库恢复，不应把数据库回滚成“无主题”。
6. 进程启动时优先读取数据库 active 记录，复核 checksum 和模板；必要时修复 active.json。数据库记录无效时记录原因并使用 fallback。
7. Rollback 只回到激活历史中的上一个有效主题或明确的默认 fallback，不能无条件把当前运行主题替换为 fallback。

如果单靠 themes 表无法可靠保存 previous theme，则新增一个小型、追加式的主题激活历史表；该表只保存主题 ID、版本、校验摘要、操作状态和时间，不保存模板内容或秘密。迁移前必须先决定是否需要该表，并为中断重启编写测试。

### 5.6 插件公开路由采用“命名空间或宿主扩展槽”

插件不应直接获得任意宿主路由。契约分两种：

- 管理路由由 Host.AdminRoute 自动落在 /admin/plugins/{plugin-id}/ 下；插件提供的相对路径不得跳出该前缀。
- 公开路由必须使用 /plugins/{plugin-id}/ 下的插件命名空间，或声明并申请一个核心拥有的扩展槽。现有 /posts/{slug}/comments 这类评论入口属于宿主扩展槽，由 comment_provider 互斥规则保证同一时间只能有一个实现。

路由注册表应记录 HTTP 方法、规范化路径、拥有者、扩展槽和处理器。注册时拒绝：

- 绝对 URL、双重斜线、路径穿越、宿主保留路径和未声明的动态段；
- 与核心路由或其他插件路由的重复；
- 没有互斥能力声明却申请已占用扩展槽；
- 菜单路径不在所属插件管理命名空间内。

插件初始化应先在临时注册表中完成，所有注册成功后再一次性合并到宿主；初始化失败必须丢弃临时路由、菜单、事件订阅和任务处理器。重新启用已初始化插件不得重复追加注册项。

### 5.7 插件设置、事件和任务必须可升级

插件 manifest 与数据库状态应共同表达：

- 稳定 plugin ID、宿主 API 版本、插件版本、kind 和能力集合；
- 设置/配置 schema 版本，以及从旧版本迁移到当前版本的规则；
- 事件订阅和任务处理器支持的名称与版本。

设置规则：

- 值在宿主侧完成类型、长度、数值、选项、URL 和总 JSON 大小校验；插件不能通过设置绕过宿主边界。
- schema 升级必须是显式迁移；迁移失败时插件保持禁用，旧值保留，错误可在管理端查看。
- 读取设置时敏感字段只返回是否已配置或脱敏占位符；保存未改变的占位符不能覆盖原秘密。
- 禁用、升级或重新启动不删除未知字段和旧值；只在迁移成功后改变 schema_version。

事件/任务规则：

- 插件只能订阅宿主已声明的事件版本，或通过明确的插件事件注册接口声明自己的版本。
- 事件名、版本、对象公共 ID、payload 字段和总大小均有上限；不能把内部行 ID、秘密、完整邮箱、正文或任意 JSON 大对象作为契约。
- 任务 kind 自动带有 plugin:{id}: 前缀，处理器注册具有唯一性；PayloadVersion 不再无条件硬编码为 1，而由定义和解码器共同管理。
- 每个持久任务必须有幂等键；旧版本任务在兼容期内有解码器，无法解码时进入可见的阻塞/失败状态，不忙循环。
- 插件禁用时不消费其已有事件/任务；重新启用后的继续消费策略由任务版本和业务幂等键决定。
- 事件和任务处理器失败必须被统一队列记录、限制重试、脱敏并可人工重试；插件不能自行创建无限 worker 或无界队列。

## 6. 实施切片与依赖

### 6.1 S0：契约冻结与基线复核

交付内容：

- 固定封面字段、历史修订版本 0/1、媒体引用并集、公开降级和导入缺失策略。
- 固定主题 settingsVersion、内容 checksum、数据库权威激活和回退目标语义。
- 固定插件公开路由槽、命名空间、设置迁移、事件/任务版本和禁用策略。
- 记录当前主题未提交改动为开发输入，不将其混入本次计划文档提交。
- 建立阶段二测试基线和一组无主题/默认主题的内容 fixture。

出口：实现过程中不再通过“临时加字段”改变上述语义；若需要改变，先更新本计划并增加相应 ADR。

### 6.2 S1：媒体与 Publishing 封面闭环

推荐实现顺序：

1. 新增 `00014_phase2_content_cover.sql`，增加修订封面快照字段和版本标记；完成空库、版本 13 数据库升级和重复打开/重启测试。
2. 扩展 publishing.Article、DraftInput、Revision、EditingSnapshot 和内部 revisionInput。
3. 在 CreateDraft、UpdateDraft、RestoreRevision、Publish、Schedule、Unpublish、Trash/Restore 的事务中统一写入当前封面、修订快照和媒体引用。
4. 将媒体引用仓库改为一次事务内重建正文与封面引用；同一内容的当前封面和已发布封面都必须受到删除保护。
5. 添加封面媒体选择器、清除封面、预览缩略图和无 JavaScript 提交流程；POST 继续覆盖 Owner、CSRF、Origin、版本冲突和输入大小策略。
6. 让公开查询只从已发布修订构造封面；管理查询只从当前修订构造封面；两者都通过有限媒体 view builder。
7. 扩展 archive manifest、archive verify/import、offline importer 的媒体/封面字段。无法取得媒体时报告可定位警告或冲突，不静默制造失效 URL。
8. 检查 content API、Feed、搜索卡片、首页、目录和主题模板的封面字段是否全部来自同一个公开视图。

切片出口：

- 上传图片 → 编辑器选择 → 保存草稿 → 发布 → 首页/列表/文章页显示的封面一致。
- 换封面、恢复修订、取消发布、垃圾箱恢复和重新发布不会把草稿封面泄露到公开端。
- 正文引用或当前/已发布封面仍在使用时，媒体删除返回安全冲突；引用清除后才允许删除。
- 归档导出、校验、导入和缺失媒体报告可重复执行。

### 6.3 S2：主题契约、设置和激活安全

推荐实现顺序：

1. 扩展 ThemeManifest 和 SettingDefinition，固定 settingsVersion、设置值边界、媒体设置和敏感值禁止进入模板的规则。
2. 添加 ThemeSettingsService 或等价窄接口，读取 schema、默认值和当前值，返回只读 ThemeSettingsView。
3. 在主题渲染所有入口注入已校验设置；模板只消费 view model，不允许访问原始配置。
4. 添加主题设置管理页和服务端表单；变更后递增 render_epoch，保留无 JavaScript 路径并防止敏感值回显。
5. 提取固定 fixture 渲染器，覆盖 home、article、listing、directory、search、status、preview、空态、媒体态和功能开关。
6. 安装阶段保存包 checksum 与确定性目录内容 checksum；激活和启动重新复核，生成结构化 validation_report。
7. 重构 ThemeCatalog/ThemeManager 的激活顺序，使数据库权威、active.json 可修复、previous theme 可回退，并覆盖进程退出窗口。
8. 用实际主题包测试自定义模板 fallback、嵌套模板、超长 CSS/JS、坏模板、坏 manifest、路径穿越、符号链接和外部修改。

切片出口：

- 未通过 fixture 的主题不能进入 active。
- 安装目录被修改、checksum 不一致或模板解析失败时，当前已运行主题保持不变。
- 激活后刷新、进程重启和 active.json 丢失/损坏时，数据库与运行时恢复到同一主题或安全 fallback。
- 设置保存后只影响允许的模板字段，缓存失效正确，秘密和内部路径不出现在 HTML、日志或审计中。

### 6.4 S3：插件路由、设置和运行时契约

推荐实现顺序：

1. 将 Host.Route/AdminRoute 统一接入路由注册表，增加路径规范化、宿主保留路径、扩展槽和重复注册检查。
2. 为公开扩展槽建立核心声明；先迁移评论 provider，再迁移 Newsletter、Content API 等已有公共入口。
3. 将菜单路径校验、处理器守卫和初始化临时注册表接入 Registry.Enable；测试失败初始化后的完整清理。
4. 增加 manifest/config schema version 的校验和迁移入口；在 Settings 保存和读取时加入总大小、敏感字段和脱敏规则。
5. 将事件订阅和任务注册改为带版本的定义；增加已知事件白名单、任务载荷限制、解码失败状态和重复注册测试。
6. 逐一迁移 comments、notifications、webhooks、analytics、contentapi 的 manifest、设置、事件和任务定义，保持已有数据和公共行为兼容。
7. 测试禁用、再次启用、应用重启、旧任务、坏配置、provider 冲突和插件异常，确认宿主页面仍可用。

切片出口：

- 插件不能覆盖宿主或其他插件路由；评论公共槽同一时间最多一个 provider。
- 插件初始化失败不会留下半套路由、菜单、事件订阅或任务处理器。
- 禁用插件后路由不可访问、事件/任务不执行，但配置、数据和失败任务保留。
- 设置升级失败时插件安全禁用，旧值仍可恢复；敏感字段不出现在列表、日志、任务和审计。

### 6.5 S4：跨模块验收与文档收口

- 验证主题和插件都消费阶段二完成后的统一公开媒体视图。
- 验证主题切换、插件禁用和内容发布分别正确递增/读取 render_epoch。
- 对公开列表和媒体列表增加阶段三前置的资源护栏：封面闭环不得引入正文全量读取或无限变体加载。
- 更新 [view model 参考](../reference/view-models.md)、[事件参考](../reference/events.md)、[extensions 文档](../extensions.md)、[themes 文档](themes.md) 和阶段进度记录。
- 如主题激活历史、修订封面快照或插件版本注册引入新的持久化语义，新增对应 ADR，不把关键决策只留在代码注释中。

## 7. 建议的数据库迁移拆分

实施前数据库版本为 13；阶段二新增 `00014_phase2_content_cover.sql` 与 `00015_phase2_theme_contract.sql`，当前数据库版本为 15。两项迁移按内容、主题分开，未把插件运行时注册表镜像到数据库。

### 7.1 内容迁移

实际迁移内容：

- `content_revisions.cover_media_public_id`；
- `content_revisions.cover_snapshot_version`，旧修订使用版本 0；
- `editing_snapshots.cover_media_id`；
- 迁移前不伪造旧修订封面事实，新增写入使用版本 1；空库、版本 13 升级和重复启动由迁移/数据库测试覆盖。

不建议把历史封面直接复制到所有旧修订，也不建议用当前草稿封面填充已发布修订。

### 7.2 主题迁移

`00015_phase2_theme_contract.sql` 在现有字段基础上增加目录确定性 checksum，并新增小型激活历史表，覆盖：

- previous theme、candidate theme、版本和内容 checksum；
- 激活操作状态、开始/完成时间；
- 失败原因的脱敏摘要；
- 唯一操作标识和审计关联。

模板、设置值和秘密不放进激活历史表。

### 7.3 插件迁移

现有 plugin_states.config_schema_version 和 plugin_settings.schema_version 可以作为基础。只有当事件/任务版本、敏感设置状态或路由声明需要跨重启持久化时才新增字段；不要为内存路由注册表建立不必要的数据库镜像。

每个迁移必须验证：

- 空数据库从头执行；
- 版本 13 数据库升级；
- 执行中断后重启；
- 重复启动不重复写入默认值或历史；
- 旧插件设置、任务和数据仍然可读取；
- 迁移失败时插件/主题保持禁用或旧状态，不进行半完成激活。

## 8. 测试与验收矩阵

### 8.1 媒体与内容

| 场景 | 预期 |
| --- | --- |
| 新建无封面文章 | 修订记录明确无封面，公开页按无图布局渲染 |
| 新建有封面文章并发布 | 管理端、首页卡片、列表、文章页使用同一媒体 URL/尺寸/替代文本 |
| 草稿换封面但未发布 | 公开页继续使用已发布修订封面，不能泄露草稿封面 |
| 发布换封面 | 发布修订、公开页、引用保护和 render_epoch 同时更新 |
| 恢复有封面修订 | 创建新修订并恢复封面；旧修订不被修改 |
| 恢复迁移前未记录封面的修订 | 不静默删除当前封面，返回可见提示并等待确认 |
| 删除正文仍引用的媒体 | 返回安全冲突，数据库和文件不被部分删除 |
| 删除当前/已发布封面媒体 | 返回安全冲突；清除所有活动引用后才可删除 |
| 缺失响应式变体 | 回退原图，正文和图片替代文本仍可用 |
| 媒体记录不存在 | 新写入拒绝；已有公开内容降级为无图并记录可观察信息 |
| 归档导出/verify/import | 封面和媒体引用可验证；缺失媒体有警告/冲突，不伪造 URL |
| API/Feed/搜索/首页 | 不出现内部 ID、object key、草稿字段或不一致封面 |

### 8.2 主题

| 场景 | 预期 |
| --- | --- |
| 路径穿越、绝对路径、符号链接、特殊文件 | 安装拒绝 |
| 超大包、超多文件、超大解压体积 | 安装拒绝且 staging 清理 |
| 坏 manifest、未知设置类型、非法默认值 | 安装拒绝 |
| 缺少必需模板、模板语法错误 | 安装/激活拒绝，当前主题不变 |
| fixture 中出现无封面、长文本、中文、404、搜索、预览 | 全部页面可渲染且输出有上限 |
| 自定义 directory/status 模板 | 不被默认 fallback 覆盖 |
| 已安装目录被修改 | checksum 复核失败，不激活 |
| DB active 已更新但进程在 marker 前退出 | 下次启动按 DB 恢复 candidate |
| active.json 损坏或删除 | 按 DB 重建；DB 无效时使用 fallback |
| 新主题设置保存 | 类型和范围校验通过，render_epoch 递增，模板只看到允许字段 |
| 未配置/敏感设置 | 不泄露原值，不进入 HTML、日志、审计或任务 |
| 激活失败后 rollback | 回到上一个有效主题或明确 fallback，不清空有效 catalog 状态 |

### 8.3 插件

| 场景 | 预期 |
| --- | --- |
| 插件申请宿主保留路径 | 注册拒绝 |
| 两个插件申请同一路由 | 注册拒绝；互斥扩展槽只有声明的 provider 可占用 |
| 管理路由使用越界路径 | 注册拒绝 |
| 插件初始化中途失败 | 路由、菜单、事件、任务均不留下半成品 |
| 同一插件重复启用 | 不重复注册处理器、菜单或订阅 |
| 设置类型/范围/总大小非法 | 保存拒绝，原配置不变 |
| 设置 schema 升级失败 | 插件保持禁用，旧配置保留 |
| 敏感设置读取/重试/审计 | 只显示脱敏状态，不泄露原值 |
| 未知事件版本/坏 payload | 安全拒绝或进入可见失败，不调用插件业务处理 |
| 旧任务重启后执行 | 使用兼容解码器和原幂等键，不重复业务结果 |
| 插件禁用后已有任务 | 不执行、不删除，任务状态仍可观察 |
| 插件重新启用 | 按策略继续消费，重复投递不重复业务写入 |
| provider 冲突 | 第二个 provider 不能启用，原 provider 数据不受影响 |
| 插件处理器 panic/错误 | 宿主请求和其他插件仍可用，失败按统一任务语义记录 |

### 8.4 基线、性能和安全

- 内容列表、主题 fixture 和插件状态查询不能读取未授权的草稿、秘密、原始请求体或内部路径。
- 封面改造不得把 Markdown 处理、图片变体生成或网络请求放入 SQLite 写事务。
- 媒体列表和主题 fixture 的项目数、变体数、输出大小和并发数有上限；阶段三再做完整 N+1 与内存专项。
- 公开主题页面继续满足无 JavaScript 基础路径、键盘焦点、可见焦点环、ARIA 状态和窄屏不横向溢出的要求。
- 日志、审计、任务错误和浏览器测试输出不得包含密码、session、TOTP、恢复码、token、完整邮箱、Webhook secret、正文或完整访客隐私数据。

## 9. 工程门禁

从仓库根目录执行，并始终使用项目固定构建标签：

- 受影响包的定向测试：media、publishing、presentation、extensions、archive、importer。
- 全量 Go 测试：make test。
- 竞态测试：make test-race。
- 静态检查：make vet。
- 数据库依赖校验：go mod verify。
- 文档/补丁检查：git diff --check。
- 主题、管理端或公共页面变化：make browser；发布前使用 BROWSER_STRICT=1 make browser。
- 公开视图、主题资源或媒体变化：make perf-gate。
- 阶段验收：make stage3-acceptance；Compose 可用时补充真实健康检查和首页/文章路径验证。

如果 Docker、Playwright 或 Go 环境不可用，使用仓库已有 Docker/script fallback，并在阶段进度记录中明确写出未运行的门禁。不能把仅有单元测试描述为浏览器或生产运行时验收。

## 10. 失败处理、迁移和回滚

### 10.1 内容数据

- 迁移只向前；生产不能使用 Goose Down 做降级。
- 新字段先兼容读取，再切换写入；旧修订的未记录状态必须可识别。
- 封面保存失败时，内容写入和引用更新一起回滚；不能出现内容已保存但引用未保存。
- 导入遇到缺失媒体时不自动访问网络补齐；报告缺失并保持导入结果可审查。

### 10.2 主题状态

- 坏包、坏模板、checksum 不一致和 fixture 失败都不能覆盖当前主题。
- 激活后的 marker 失败按可修复运行时状态处理；下次启动以数据库 active 为准。
- 主题回退保留安装目录、设置和历史审计；只改变当前 active 和运行时指针。
- 任何无法确认数据库/运行时状态的故障都使用嵌入式默认主题，并在管理端显示待处理状态。

### 10.3 插件状态

- 插件设置迁移失败时保留旧 JSON 和旧 schema_version，插件不启用。
- 禁用插件只停止路由、事件和任务处理；不删除队列、数据、投递记录或设置。
- 无法解码的旧任务进入可见失败/阻塞状态，不能通过无限重试掩盖版本不兼容。
- 插件注册失败不影响核心路由、默认主题和其他已启用插件。

## 11. 推荐 Git 分批方式

本节作为实现和后续 Git 拆分参考。本轮只完成工作树内的实施与验收记录，未执行新的 Git 提交或推送；用户既有主题目录、主题测试和运行产物继续保持独立。

建议提交顺序：

1. docs: add phase-two implementation plan
2. feat(database): add content cover snapshot migration
3. feat(media): unify body and cover references
4. feat(publishing): carry cover through revision and publication lifecycle
5. feat(presentation): expose published media view and cover rendering
6. feat(archive): preserve cover metadata in export and import
7. feat(themes): add settings and fixture validation contract
8. feat(themes): make activation checksum and rollback recoverable
9. feat(extensions): enforce plugin routes and registration isolation
10. feat(extensions): version plugin settings, events and tasks
11. test(phase-two): add cross-module failure and restart acceptance
12. docs: record phase-two completion and residual risks

当前未提交的主题开发文件应继续与其他批次隔离；除非另行确认，不要在内容、媒体或插件提交中顺带加入这些文件。

## 12. 阶段二完成条件

以下条件用于标记路线图中的阶段二核心实现完成；外部环境依赖的未运行门禁必须在进度记录中明确列出，不能以代码测试替代。

### 内容

- 封面可由管理端安全选择、清除、预览和保存。
- 草稿、编辑快照、修订、发布、恢复、垃圾箱和定时发布的封面语义一致。
- 公开端始终读取已发布修订，不读取草稿封面。
- media_references、媒体删除保护、媒体变体回退和归档/importer 通过失败测试。
- API、Feed、搜索、首页、列表和主题都使用统一的公开媒体视图。

### 主题

- manifest、settingsVersion、设置校验和 ThemeSettingsView 有文档和测试。
- 所有代表性 fixture 渲染通过，坏包不能替换当前主题。
- 安装 checksum、目录内容 checksum、数据库 active、active.json 和运行时内存状态可在重启后收敛。
- 回退回到上一个有效主题或明确 fallback；主题设置和审计数据不丢失。
- 主题页面通过无 JavaScript、浏览器和相关性能门禁。

### 插件

- 公开路由只能位于插件命名空间或声明的宿主扩展槽；宿主/插件/插件之间无未声明冲突。
- 初始化失败、重复启用、禁用、重启和 provider 冲突测试通过。
- 设置 schema 版本迁移、敏感值脱敏、事件/任务版本和坏载荷处理有证据。
- 插件任务和事件遵守统一幂等、租约、重试、禁用和失败可见性。
- 核心站点在单个插件失败或禁用时继续可用。

### 工程与文档

- 固定构建标签下的定向测试、make test、make test-race、make vet 和 git diff --check 通过。
- 适用时通过 make browser、BROWSER_STRICT=1 make browser、make perf-gate 和 make stage3-acceptance。
- 数据库迁移覆盖空库、版本 13 升级、失败重启和重复启动；当前版本为 15。
- [架构](../architecture.md)、[数据模型](../data-model.md)、[主题开发文档](themes.md)、[插件开发文档](plugins.md)、[视图模型](../reference/view-models.md)、[事件参考](../reference/events.md) 和阶段进度记录与实现一致。
- 阶段进度记录写明实际 Git 提交、已运行门禁、未运行门禁、真实运行时证据和剩余风险。

## 13. 阶段二执行清单

实施开始前：

- [x] 确认 S0 的封面历史兼容策略、主题激活权威和插件公共路由槽。
- [x] 以版本 13 为升级基线，保留旧修订未记录封面状态，不伪造历史事实。
- [x] 固定阶段二 fixture、测试媒体和坏主题/坏插件样本。
- [x] 确认当前未提交主题文件不进入其他模块提交。

实施过程中：

- [x] 每个切片先补失败路径和重启/恢复测试，再接入 HTTP/CLI。
- [x] 每个改变公开输出的写路径检查 render_epoch。
- [x] 每个新持久化字段有空库、升级库和重复打开/恢复测试。
- [x] 每个公开字段检查是否误带内部 ID、草稿、秘密或文件路径。
- [x] 每个外部副作用保持在事务外，并使用现有 durable task 边界。

实施结束前：

- [x] 完成第 8 节适用的代码与本地运行时验收场景；浏览器和 Compose 依赖项的未运行原因已登记。
- [x] 完成第 9 节可执行工程门禁并保存结果。
- [x] 更新文档、ADR 和 `docs/progress/` 阶段记录。
- [x] 复查工作树，确认运行产物、Playwright 目录和用户主题文件未被纳入实现边界。
- [x] 保留主题相关改动的独立提交边界，等待用户决定后续提交。

## 14. 阶段二实施与真实验收记录

### 14.1 实施结果

| 切片 | 实际结果 | 证据 |
| --- | --- | --- |
| S0 契约冻结 | 封面快照版本 0/1、主题 DB 权威、插件路由槽/命名空间和版本化设置/任务契约已固定 | 本文、ADR-0038、数据模型/扩展文档 |
| S1 内容与封面 | 封面贯通编辑、修订、发布、恢复、引用保护、公开媒体视图、RSS、归档和离线导入；缺失媒体导入报告冲突 | `internal/media`、`internal/publishing`、`internal/archive`、`internal/importer` 测试 |
| S2 主题 | 设置版本/schema、敏感值脱敏、媒体安全视图、确定性目录 checksum、六类 fixture、激活/回退/marker 修复已实现 | `internal/presentation/theme_test.go`、主题包测试 |
| S3 插件 | 路由槽和命名空间、临时注册隔离、设置迁移/脱敏、事件版本、任务载荷/幂等边界和禁用语义已实现 | `internal/extensions/host_test.go` |
| S4 收口 | 文档、ADR、迁移版本和验收证据已同步；阶段一 SQLite 清理语法问题已修复 | `docs/adr/0038-phase-two-content-and-extension-contracts.md`、启动日志 |

### 14.2 工程门禁

| 命令 | 实际结果 |
| --- | --- |
| `make build` | 通过，使用 `fts5 sqlite_omit_load_extension` |
| `make test` | 通过，全量 Go 包 |
| `make test-race` | 通过，全量 Go 包 |
| `make vet` | 通过 |
| `go mod verify` | 通过：`all modules verified` |
| `make perf-gate` | 通过：ADR-0031 in-process performance gate |
| `git diff --check` | 通过 |
| `make stage3-acceptance` | 脚本退出 0；Go/race/vet/module 检查通过，`docker compose config` 可解析；Docker daemon `ps` 因权限拒绝，Compose 运行态分支未执行，已由下方本地服务操作替代 |
| `make browser` | 未运行完成：npm 探测无响应后中止；离线确认 `@playwright/test` 未缓存，且本机无 Chromium/Firefox 可执行文件 |

### 14.3 本地服务实际操作

使用最终 `bin/blog` 启动：`tmux new-session -d -s blog-project './bin/blog serve --config config.example.toml'`。

- 监听端口：`:8080`。
- 启动日志：迁移版本 15、`server listening address=:8080`，未出现 `near LIMIT`、panic 或启动错误。
- `./bin/blog healthcheck -url http://127.0.0.1:8080/readyz`：通过。
- `./bin/blog status --config config.example.toml`：迁移版本 15，`pending_jobs/running_jobs/failed_jobs` 均为 0，已有有效备份 1 个。
- 实际 HTTP：`/livez`、`/readyz`、`/`、`/search`、`/rss.xml`、`/sitemap.xml`、`/robots.txt`、`/admin/login` 均返回 200；未登录 `/admin/plugins` 和 `/admin/themes` 返回 303；伪造媒体路径返回 404。
- 可选内容 API 在示例配置中默认关闭，`/api/v1/site`、`/api/v1/posts`、`/api/v1/pages` 返回 404，确认未默认暴露入口。
- 首页响应未发现 `googleapis`、`gstatic`、`cdnjs`、`unpkg` 或 CDN 外部资源。

### 14.4 工作树与剩余风险

- 本轮未执行新的 Git 提交或推送；阶段一已有提交保持不变。
- `.playwright-results/`、`themes/cel-panel/` 和 `internal/presentation/cel_panel_theme_test.go` 等用户既有文件继续保留，未作为阶段二其他模块的提交内容处理。
- 浏览器三引擎和 Docker Compose app/caddy 运行态仍需在具备 Playwright 浏览器和 Docker daemon 权限的环境中补验；本地服务的真实 HTTP/CLI 验收已完成。
