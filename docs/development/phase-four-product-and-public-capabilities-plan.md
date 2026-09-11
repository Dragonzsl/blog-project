# 改进阶段四实施计划：产品体验与公共能力

> 状态：设计中，尚未实施
>
> 编制日期：2026-09-11
>
> 上游路线图：[项目现状评估与改进路线图](../project-assessment-and-improvement-roadmap.md)
>
> 前置阶段：[阶段一生产安全与一致性](../progress/phase-one-production-hardening.md)、[阶段二内容与扩展契约](../progress/phase-two-content-and-extension-contract.md)、[阶段三规模与性能实施记录](../progress/phase-three-performance-implementation.md)
>
> 本阶段不包含主题插件本身的提交、主题视觉重做或新的基础设施引入。主题插件和已有运行产物继续保持独立边界。

## 1. 阶段定位与目标

阶段四是阶段一至三完成后的产品体验与公共能力收口阶段。前置阶段已经建立了发布生命周期、不可变版本、封面引用、主题/插件契约、公开读取投影、缓存、任务、备份和基本 API；本阶段不重新设计这些基础边界，而是把高频管理操作和对外契约做成可持续使用的产品能力。

本阶段目标：

1. 让站主可以在后台完成日常安全维护和站点元数据维护，不依赖 CLI。
2. 让版本比较、恢复确认、批量内容处理和定时发布具备清晰、可回退的操作路径。
3. 把评论和 Newsletter 从“可运行插件”提升为可审计、可管理的生命周期能力。
4. 为只读内容 API 增加稳定游标和增量同步语义，同时保持现有 v1 客户端兼容。
5. 完善 canonical、社交卡片、结构化数据、RSS/Feed 和条件缓存输出。
6. 使归档能表达媒体引用、重定向和允许导出的站点配置，并能在新实例中验证导入。

### 1.1 阶段出口

阶段四完成必须同时满足：

- 站主可在后台修改密码、轮换 TOTP/恢复码、撤销会话和维护站点公开设置；敏感动作有重新认证、审计和明确的失效语义。
- 修订比较、恢复前预览、批量操作和排程日历均保留无 JavaScript 基础路径；恢复旧版本仍创建新版本，不改写历史。
- 批量操作有固定批次上限、幂等/重放保护、逐项授权检查、部分失败结果和审计记录。
- 评论管理和 Newsletter 生命周期覆盖筛选、批量处理、双重确认、签名退订和脱敏状态展示；不向后台模板输出邮箱明文。
- Content API 的 cursor、过滤、updated_since、ETag 和 Last-Modified 语义有版本化文档与兼容测试；大页码不会导致无界查询。
- 公开页面和 Feed 的 SEO 输出经过固定样例验证，JSON-LD/社交元数据不会引入原始 HTML 或未授权媒体 URL。
- 归档 manifest、媒体引用、重定向和允许导出的站点设置可验证；导入失败不会覆盖当前权威数据。
- 全量测试、race、vet、性能门禁、浏览器/无 JavaScript 回归和隔离归档往返均有实际记录；环境不可用时明确列出未执行项。

## 2. 当前基线与缺口

| 领域 | 当前已有 | 阶段四缺口 |
| --- | --- | --- |
| Publishing | 草稿、发布、定时发布、撤回、回收站、编辑快照、不可变版本和恢复 | 没有版本 diff、恢复前预览、批量内容操作和排程日历视图 |
| Identity/Site | 唯一站主、密码/TOTP、恢复码、会话、CLI 恢复和审计基础 | 日常密码/TOTP/恢复码/会话维护及站点设置缺少完整后台入口 |
| Comments | 本地/外部提供方边界、审核、回复、清洗、限流、幂等和通知任务 | 管理筛选、搜索、批量审核/垃圾处理、分页和更完整审计视图 |
| Newsletter | 确认 token、退订 token、加密邮箱、Provider 同步任务和幂等 | 管理状态、重新发送确认、双重确认体验、退订审计和脱敏列表 |
| Content API | 默认关闭的只读 v1 API、已发布投影、Bearer、基础 page/per-page、ETag/304 | 稳定 cursor、过滤、updated_since、条件缓存细节和大数据集查询边界 |
| Discovery | canonical、RSS、Sitemap、robots、llms.txt、基础 SEO 字段和重定向 | 社交卡片、结构化数据、Feed 条件缓存和站点级 SEO/社交设置收口 |
| Archive | 内容归档、校验、导入、媒体/封面基础支持和恢复边界 | manifest 完整表达媒体引用、重定向、允许导出的站点配置及导入前报告 |

阶段一至三已完成的代理身份、公开写入保护、事件 outbox、任务租约、封面引用、主题/插件契约、轻量投影、缓存合并、备份加密和运维摘要不在本阶段重复实现。

## 3. 范围与非目标

### 3.1 纳入范围

- F1 Owner 与站点设置后台。
- F2 修订比较、恢复预览与版本操作体验。
- F3 批量内容操作与排程日历。
- F4 评论管理与 Newsletter 生命周期产品化。
- F5 Content API 增量同步和条件缓存契约。
- F6 SEO、社交卡片、结构化数据与 Feed 收口。
- F7 归档 manifest、媒体引用、重定向和安全配置导出。
- F8 统一验收、迁移兼容、文档和发布门禁。

### 3.2 明确不纳入

- 多用户、角色/权限、读者账户、会员体系或多租户。
- 公开写 API、GraphQL、实时协作编辑或新的自定义内容类型。
- SPA、全局客户端状态、Redis、消息队列、独立搜索服务或微服务拆分。
- 在线主题/插件市场、任意脚本执行、主题服务端代码执行。
- 支付、复杂推荐、社交网络和与个人出版核心无关的增长功能。
- 本阶段不继续扩大阶段三性能优化范围；已知冷渲染和搜索尾延迟作为风险记录，不以提高资源预算掩盖。

## 4. 不可改变的架构与安全约束

- 系统仍是单站点、单 Owner；不引入通用用户表、角色表或成员关系。
- Markdown、已发布修订和稳定 permalink 仍是权威来源；diff、预览、API、SEO、Feed 和归档均为其派生视图。
- 公开读取只能使用已发布修订；编辑快照、草稿和后台字段不得进入公开 API、Feed、JSON-LD 或主题渲染。
- HTTP handler 不直接写 SQL；跨模块读取通过服务接口、只读投影或版本化事件完成。
- 每个业务写入保持一个短 SQLite 事务；邮件、Webhook、媒体处理、Markdown 重算和大批量归档均在事务外执行。
- 所有批量操作、游标、缓存、任务和导出都有固定大小、时间和响应体上限。
- 站点设置、导航、主题设置、发布状态和公开元数据变化必须提高 render_epoch，并保持缓存失效语义。
- 新增 POST/PUT/DELETE 必须覆盖 Owner 授权、CSRF、Origin、重放/幂等、错误响应和审计。
- 密码、会话、TOTP、恢复码、API 密钥、邮箱明文、token 和完整访客隐私数据不得进入日志、模板、审计或归档。
- API cursor 只表达稳定排序位置，不暴露 SQLite 行号；公开 URL 继续使用 slug 或稳定 public_id。

## 5. 目标设计

### 5.1 F1：Owner 与站点设置

**目标**：把日常安全维护和公开站点设置从 CLI 收口到受保护后台。

#### 5.1.1 设置分组

建议按风险拆为三个页面/表单，而不是一个无界设置 JSON：

1. **站点公开设置**：名称、主要语言、时区、公开基础 URL、描述、默认 SEO 标题/描述、社交链接和默认社交图片公共 ID。
2. **Owner 安全设置**：当前密码验证、新密码、TOTP 重新绑定、恢复码重新生成、其他会话撤销。
3. **公开能力设置**：Feed 摘要策略、评论/Newsletter/Content API 的启用状态和必要的非秘密展示配置；秘密继续来自环境变量或受限 secret 引用。

#### 5.1.2 安全语义

- 修改密码、TOTP、恢复码和撤销全部会话前必须重新验证当前密码与 TOTP；成功后提高 owners.auth_version，旧会话立即失效。
- TOTP 重新绑定采用一次性 challenge，验证成功后才替换旧密钥；失败不改变当前认证状态。
- 恢复码只在成功生成时展示一次，数据库保存哈希；重新生成会使旧恢复码全部失效。
- 站点基础 URL、SEO 和社交设置保存后递增 render_epoch，并在审计中记录字段类别，不记录敏感值。
- 表单保留无 JavaScript 提交路径；敏感操作成功后清理表单值并回到安全设置页。

#### 5.1.3 出口条件

- 未登录、CSRF 错误、Origin 不可信、当前密码错误和 TOTP 错误均有统一失败响应。
- 密码/TOTP/恢复码修改后旧会话无法访问后台；新会话可按正常流程登录。
- 站点设置影响 canonical、Feed、Sitemap、JSON-LD 和社交卡片，且不会让公开缓存继续使用旧设置。

### 5.2 F2：修订比较、恢复预览与版本体验

**目标**：利用已有不可变 content_revisions 提供可理解的历史操作，不改变版本权威语义。

#### 5.2.1 比较模型

- 版本列表继续使用轻量摘要；用户选择两个正式版本后，单独读取正文进行比较。
- 元数据按字段比较：标题、摘要、slug、SEO、分类、标签、封面和发布时间；正文使用 Markdown 行级 diff，必要时再提供词级强调。
- diff 输出全部是模板转义后的普通文本；不能把 Markdown、HTML 或主题输出当作安全 HTML。
- 编辑快照只能作为“当前未保存内容”提示或恢复来源，不显示为正式版本，也不写入版本历史。

#### 5.2.2 恢复预览

- 恢复动作先展示目标版本摘要、当前编辑状态和预计影响；预览页面使用 no-store，不能进入公开页面缓存。
- 确认恢复仍调用现有 RestoreRevision 语义：创建新的 reason=restore 版本、检查 lock_version、更新当前编辑状态并审计。
- 如果目标版本属于已发布检查点，恢复编辑状态不等于自动发布；页面必须明确“恢复为草稿/当前编辑内容”。
- 已发布内容的历史 permalink、重定向和封面引用不能因为恢复旧版本被删除或复用。

#### 5.2.3 出口条件

- 版本比较覆盖空正文、长中文、代码、表格、长 URL、封面变化和分类/标签变化。
- 并发编辑时恢复使用乐观锁；冲突不得覆盖新保存内容。
- 版本正文不进入版本列表查询，diff 大小有上限，超限时给出安全提示而不是无界加载。

### 5.3 F3：批量内容操作与排程日历

**目标**：减少逐篇维护成本，同时把危险动作限制在可观察、可回退范围内。

#### 5.3.1 批量操作范围

第一版仅支持已有单项语义的组合：

- 草稿/定时内容：批量设置或取消定时、批量移入回收站。
- 已发布内容：批量撤回、批量移入回收站。
- 回收站：批量恢复；永久清理继续走已有有界清理策略，不提供无确认的全量删除。
- 分类、标签和封面不在批量操作中直接隐式修改，避免产生不可逆的内容重写。

#### 5.3.2 批量事务与幂等

- 单次同步请求最多处理固定数量（建议 50）项；超过上限拒绝并提示分批，不把大选择集一次加载进内存。
- 每项重新读取当前状态和 lock_version，状态不适用或已被其他请求修改的项单独记为 skipped/conflict。
- 请求使用操作级幂等键；同一操作重放返回之前的汇总，不重复发布、通知、事件或审计。
- 每个内容项仍使用独立短事务；成功项提交后通过现有事件/任务边界处理搜索、缓存、Webhook 和通知。
- 结果包含 succeeded、skipped、conflict、failed 的数量和安全的公开 ID/标题摘要；不返回正文和秘密。
- 危险批量操作要求 CSRF、Origin、二次确认和审计；失败不得通过自动无限重试掩盖。

#### 5.3.3 排程日历

- 日历只读已有 scheduled 内容，按站点时区显示，数据库仍以 UTC Unix milliseconds 保存。
- 月/周视图使用有界日期范围和固定最大条目数；超出范围必须分页或按天展开，不能扫描全部内容。
- 点击日历项回到既有编辑页；定时修改继续使用单项乐观锁，不在日历页面复制发布逻辑。
- 时间冲突、夏令时、不合法本地时间和已过期计划均给出明确提示；不自动替用户改写时间。

#### 5.3.4 出口条件

- 批量操作在重复提交、进程中断、单项冲突、任务失败和重启后均可解释、可恢复。
- 50 项上限、查询次数、响应字节和审计数量均有测试证据。
- 桌面、窄屏、无 JavaScript、键盘选择和焦点回收均通过。

### 5.4 F4：评论管理与 Newsletter 生命周期

#### 5.4.1 评论管理

- 管理页支持状态、文章、时间范围和关键词筛选；列表按 cursor 或稳定 (created_at, id) 顺序分页。
- 支持单项和有界批量审核、标记垃圾、移入回收站、恢复；批量结果遵循 F3 的部分成功和幂等语义。
- 搜索只匹配显示名、文章标题和安全摘要；邮箱仅用于内部动作，不展示明文、不写入 URL。
- 回复深度、文章归属、父评论状态和已发布文章状态继续由服务层校验；管理操作不绕过评论清洗和事件通知。
- 审计记录对象 public_id、旧/新状态和结果，不记录正文、邮箱和客户端原始地址。

#### 5.4.2 Newsletter

- 保留现有双重确认：订阅请求进入 pending，确认 token 一次性消费后才变为 active。
- 后台只显示脱敏地址、状态、创建/更新时间、确认时间（若补充）和 Provider 同步状态；不提供邮箱明文导出。
- 允许站主对 pending 订阅重新发送确认，但有时间窗、幂等键和发送频率上限。
- 退订始终使用过期时间和用途绑定的签名 token；重复退订应幂等，不能凭邮箱地址直接退订。
- Provider 同步失败继续进入持久任务并在后台显示状态；不回滚本地合法的订阅/退订决定。
- 订阅、确认、退订和重新发送写入脱敏审计；token 原文不入日志、模板快照或归档。

#### 5.4.3 出口条件

- 评论和 Newsletter 在默认关闭插件时不注册路由、不执行任务、不增加公开资源。
- 启用/禁用、重启、重复确认、过期 token、Provider 失败和人工重试均有测试。
- 管理页面没有邮箱明文、评论 token、任务 payload 或外部 Provider secret。

### 5.5 F5：Content API 增量同步与条件缓存

**目标**：在保持 /api/v1 兼容的前提下，将 page/per-page 查询扩展为有界、可增量同步的公开读取协议。

#### 5.5.1 查询协议

- 保留现有 page/per_page 作为兼容路径；新增 cursor 后，禁止同时使用 page 参数，冲突返回 400。
- cursor 为带版本的 opaque base64url 值，内部表达稳定排序 tuple（例如 published_at 与 public_id）；不暴露内部 SQLite ID。
- 默认排序固定且文档化；同一时间戳使用 public_id 作为确定性 tie-breaker，避免翻页重复或遗漏。
- 新增 kind、category、tag、updated_since 等过滤器时设置最大长度、最大日期跨度和最大返回数量。
- updated_since 只返回当前已发布内容；撤回/删除的 tombstone 语义若本版无法兼容，不伪造删除记录，必须在 API 文档中明确限制。
- 返回 next_cursor、has_more、服务端生成时间和协议版本；响应体有固定上限。

#### 5.5.2 缓存和认证

- 公开、无 Bearer 的 GET 响应提供稳定 ETag 和 Last-Modified；匹配条件请求返回 304 且无响应体。
- 带 Bearer 的响应默认 private 或 no-store，除非明确证明 token 不改变表示；不把授权响应写入共享页面缓存。
- Token 比较使用常量时间比较；token 解析、cursor 解码和错误响应不泄露有效 token 存在性。
- API 继续只返回已发布投影；列表不因 page 很大而先读取 limit * page 行，查询必须直接使用游标/范围条件。
- 版本升级保持旧字段和状态码语义；新增字段只向后兼容，破坏性变化进入新的 API 主版本。

#### 5.5.3 出口条件

- 1 万篇文章下连续翻页无重复/遗漏；在发布、撤回、slug 变化和 epoch 变化后行为有明确测试。
- cursor 过期、篡改、过长、跨过滤器复用和 page/cursor 冲突均安全失败。
- ETag、Last-Modified、Bearer、错误响应和限流均有 HTTP 集成测试。

### 5.6 F6：SEO、社交卡片、结构化数据与 Feed

#### 5.6.1 公开元数据

- 文章/页面输出 canonical、title、description、Open Graph、Twitter/X card 和适用的 og:image 安全媒体 URL。
- 文章使用 BlogPosting 或等价结构化数据；页面使用 WebPage；站点、面包屑和作者字段只来自安全公开视图。
- JSON-LD 必须由 Go 结构体序列化生成并作为安全 JSON 输出，禁止把正文 HTML、用户输入或任意模板片段拼接进脚本。
- 草稿、预览、后台、搜索结果和错误页继续 noindex/no-store，不因新增 SEO 模块而改变缓存隔离。
- 缺失封面、坏媒体或超大描述时使用安全降级；不能阻断文章正文。

#### 5.6.2 RSS/Feed 与条件请求

- 保留现有 RSS 路径和稳定 guid/permalink 语义；补齐站点描述、语言、最后更新时间、文章摘要/封面（若协议允许）和规范链接。
- Feed 根据已发布修订生成，撤回内容不再进入后续输出；生成失败不读取草稿或编辑快照。
- RSS、Sitemap、robots、llms.txt 和公共页面共享站点设置与 render_epoch 失效；支持合理的 ETag/Last-Modified。
- Feed 输出设固定条数/正文长度上限；大正文不在一次请求中无界拼接。

#### 5.6.3 出口条件

- 中英文长标题、特殊字符、无封面、坏媒体、长描述和 HTML 注入样例均通过解析和安全断言。
- canonical、OG、JSON-LD、RSS、Sitemap 与重定向在 slug 修改、撤回、站点设置修改后保持一致。
- 页面缓存、Feed 条件请求和 noindex 语义没有回归。

### 5.7 F7：归档完整性与安全配置导出

- manifest 增加格式版本、应用/迁移版本、站点公开身份、内容/修订/媒体/重定向数量和每项 checksum；不包含密码、会话、TOTP、恢复码、API 密钥、Provider secret 或邮箱明文。
- 归档内容表达稳定 public_id、slug、修订、分类/标签、封面媒体引用、正文中的媒体引用、重定向和允许导出的站点公开设置。
- 导出前执行有界一致性扫描：引用缺失、重复公共 ID、路径冲突、坏 checksum 和不允许的秘密字段均报告为失败或警告。
- 导入先做 dry-run 和完整性校验，再在隔离暂存目录生成草稿/待确认数据；不得直接覆盖现有已发布内容。
- 媒体引用缺失时生成可操作冲突报告，不写入失效公开 URL；重定向导入继续做循环检测和目标压平。
- 导入中断、重复导入、版本不兼容和 checksum 失败都能安全重启或放弃，不修改原始归档和当前权威站点。

#### 出口条件

- 空库导入、升级库导入、重复导入、缺失媒体、坏归档、路径穿越、符号链接和秘密字段扫描均有测试。
- 导入后文章、页面、版本、封面、重定向和公开输出可逐项对照；失败不会污染现有数据。

## 6. 实施切片与依赖

~~~text
Q0 契约冻结与样例矩阵
       ├── Q1 Owner/站点设置 ───────┐
       ├── Q2 版本比较/恢复预览      │
       ├── Q3 批量操作/排程日历 ─────┤
       ├── Q4 评论/Newsletter ───────┤
       ├── Q5 Content API 增量协议 ───┤
       └── Q6 SEO/Feed ──────────────┤
                                    ↓
                         Q7 归档完整性
                                    ↓
                         Q8 阶段验收与发布记录
~~~

| 切片 | 目标 | 必须性 | 主要模块 | 出口 |
| --- | --- | --- | --- | --- |
| Q0 | 固定协议、页面、错误和样例 | 必须 | docs、tests/fixtures、各模块接口 | 无未决字段语义和兼容策略 |
| Q1 | Owner/站点设置 | 必须 | identity、presentation、discovery、web/admin | 敏感设置可审计且旧会话失效正确 |
| Q2 | diff/预览/恢复体验 | 必须 | publishing、web/admin | 历史不可变、恢复有锁和预览隔离 |
| Q3 | 批量操作/排程日历 | 必须 | publishing、operations、web/admin | 批次有界、部分失败可见、无 JS 可用 |
| Q4 | 评论/Newsletter 产品化 | 必须 | comments、notifications、web/admin | 状态、token、Provider 失败和审计闭合 |
| Q5 | API 增量同步 | 必须 | contentapi、publishing、platform | cursor/过滤/条件缓存兼容并有大数据集证据 |
| Q6 | SEO/Feed 收口 | 必须 | discovery、presentation、web/admin | 页面、Feed、Sitemap 与设置/发布一致 |
| Q7 | 归档完整性 | 必须 | archive、media、organization | manifest、引用和冲突可验证 |
| Q8 | 综合验收 | 必须 | tests/browser、internal/perf、scripts、docs/progress | 形成真实环境证据和残余风险记录 |

Q1–Q6 可以在 Q0 后并行开发，但 Q7 应在站点设置、媒体引用和重定向字段稳定后进行；Q8 必须等待所有对外协议冻结。

## 7. 数据库、迁移与模块边界

### 7.1 迁移策略

- 当前最新迁移为 00018；实施前必须重新检查工作区并使用下一个实际空闲编号，本文不预先占用固定编号。
- diff、恢复预览、排程日历和 API cursor 原则上不需要新表；优先复用现有 content_revisions、jobs、audit_entries 和已有索引。
- Owner 安全操作优先复用 owners.auth_version、sessions 和 owner_recovery_codes；只有确认缺少确认时间/挑战状态时才新增最小字段或表。
- 站点设置使用固定字段或有版本/大小上限的设置结构；禁止引入无界任意 JSON。新增站点公开设置必须能驱动 render_epoch。
- Newsletter 若需要确认/退订时间或 Provider 状态，应补充最小可索引字段；秘密和 token 不作为明文列保存。
- 评论筛选/批量处理只在真实查询证明后增加索引；不为未来筛选预建宽索引。
- 每个迁移覆盖空库、从版本 18 升级、重复启动、失败/重启和回滚前备份验证；生产不使用 Goose Down 做数据回滚。

### 7.2 模块责任

- identity 拥有 Owner、会话、认证版本和安全设置；presentation 只消费已验证的公开站点视图。
- publishing 拥有版本、恢复、状态和排程；批量 HTTP 只是逐项调用服务，不复制状态转换 SQL。
- comments/notifications 保持插件边界；核心不直接查询插件私有表，后台菜单和路由通过 Host 注册。
- contentapi 只读取 Publishing/Organization/Presentation 提供的公开投影；不得通过 SQL 读取草稿或私有字段。
- discovery 负责 SEO、Feed、Sitemap 和条件缓存；结构化数据使用安全公开视图，不读取主题私有配置。
- archive 通过各模块的导出接口获取数据，不复制数据库文件作为归档，也不把 secret 带出应用。

## 8. 测试与验收矩阵

### 8.1 功能与安全

| 范围 | 必测场景 |
| --- | --- |
| Owner 设置 | 当前密码/TOTP 错误、重新认证、会话失效、恢复码一次性展示、重启后配置一致 |
| 版本体验 | 空正文、长中文、代码、表格、封面/分类/标签变化、并发恢复、预览不缓存 |
| 批量操作 | 50 项上限、重复提交、单项冲突、部分失败、中断重启、审计和任务幂等 |
| 排程日历 | 时区、夏令时、过去时间、跨月范围、无 JS、键盘和窄屏 |
| 评论/Newsletter | 状态筛选、批量审核、垃圾/回收站、重复 token、过期 token、Provider 失败和重试 |
| API | page/cursor 兼容、cursor 篡改、过滤器边界、updated_since、ETag/304、Bearer 和限流 |
| SEO/Feed | slug/撤回/设置变化、无封面、特殊字符、JSON-LD 注入、noindex 和条件请求 |
| 归档 | 空库/升级库、重复导入、坏 checksum、缺失媒体、重定向循环、秘密字段和路径穿越 |

### 8.2 页面与可访问性

- 公开页面、后台设置、版本 diff、批量列表、排程日历、评论管理和 Newsletter 页面覆盖桌面、平板、移动、窄屏和短视口。
- 所有危险 POST 保留无 JavaScript 路径、CSRF/Origin 检查、可见焦点环、键盘提交、ARIA 状态和错误回显。
- 批量选择不会丢失焦点；确认区域明确列出数量、状态和不可逆影响。
- diff 文本不能只依赖颜色表达；屏幕阅读器可区分新增、删除和未变化内容。
- 主题插件视觉回归不纳入本阶段提交，但公共页面的语义、缓存和 API 变化必须通过默认主题回归。

### 8.3 工程门禁

聚焦验证：

~~~bash
go test -tags 'fts5 sqlite_omit_load_extension' ./internal/identity ./internal/publishing ./internal/comments ./internal/notifications ./internal/contentapi ./internal/discovery ./internal/archive
go test -race -tags 'fts5 sqlite_omit_load_extension' ./internal/identity ./internal/publishing ./internal/comments ./internal/notifications ./internal/contentapi ./internal/discovery ./internal/archive
~~~

阶段收口至少执行：

~~~bash
make test
make test-race
make vet
go mod verify
make perf-gate
make stage3-acceptance
BROWSER_STRICT=1 make browser
git diff --check
~~~

另需记录：1 万篇文章 API cursor/updated_since 压测、归档往返报告、隔离站点恢复、Compose 资源约束、实际 Caddy/TLS 链路和无 JavaScript 页面结果。Docker 或 Playwright 不可用时，不能用单进程或静态检查替代真实环境结论。

## 9. 回滚、兼容与停止条件

### 9.1 回滚边界

- Owner 设置保存失败时不提高认证版本、不部分替换 TOTP；密码和 TOTP 变更成功后旧会话失效是不可回退的安全语义。
- diff/预览为只读派生功能，异常时关闭入口不影响发布、版本和恢复 API。
- 批量操作逐项提交；单项失败不回滚已成功项，汇总必须可追踪并支持人工复核。
- API 新增 cursor 只作为兼容扩展；异常时保留 page/per-page 读取，不改变已发布数据。
- SEO/Feed 使用新生成版本或缓存键；生成失败回退到最小安全元数据，不返回草稿或不可信 HTML。
- 归档导入在暂存目录和事务边界内完成；校验失败不交换目录、不覆盖当前站点。

### 9.2 停止推进条件

出现以下任一情况，停止进入下一切片：

- 敏感设置修改不能可靠失效旧会话，或审计包含秘密。
- diff/预览泄露草稿、编辑快照、未清洗 HTML 或内部路径。
- 批量操作产生重复发布、重复任务、重复通知或不可解释的部分成功。
- API cursor 在发布/撤回/排序变化时出现重复、遗漏或无界查询。
- SEO/Feed/归档输出包含草稿、秘密、失效媒体 URL 或不安全 JSON。
- 迁移、导入或归档失败会破坏当前已发布数据。
- 测试门禁通过但浏览器、无 JS、资源限制或重启路径出现行为回归。

## 10. Git 分批与文档产物

本计划只创建文档，不创建提交。实际实施建议按以下边界分批：

1. docs(phase4): add product and public capabilities plan
2. feat(identity): add owner and site settings workflows
3. feat(publishing): add revision comparison and restore preview
4. feat(publishing): add bounded bulk actions and schedule calendar
5. feat(extensions): complete comments and newsletter management
6. feat(contentapi): add cursor and incremental public reads
7. feat(discovery): complete SEO social metadata and feed validators
8. feat(archive): extend manifest references and safe site settings
9. test(phase4): add browser, no-js, migration and round-trip acceptance
10. docs(phase4): record implementation evidence and remaining risks

每个代码提交必须带对应失败路径测试、聚焦门禁和文档更新。themes/cel-panel/、internal/presentation/cel_panel_theme_test.go 和 .playwright-results/ 不混入以上提交，除非用户另行要求主题插件工作。

最终文档产物：

- 本文：范围、设计、实施顺序、迁移、门禁和回滚规则。
- docs/progress/phase-four-product-and-public-capabilities.md：实际实现、提交、验收证据和未完成项。
- 必要时新增 ADR：API 增量同步语义、站点公开设置 schema 或归档格式版本；不得仅在实现代码中隐含破坏性协议。

## 11. 完成判定

只有同时满足以下条件，阶段四才能标记完成：

- Owner/站点设置可以在后台安全维护，敏感变更可重新认证、失效会话并审计。
- 版本 diff、恢复预览、批量操作和排程日历在桌面、窄屏、无 JavaScript 和并发冲突下行为稳定。
- 评论、Newsletter、Content API、SEO/Feed 和归档均有版本化契约；默认关闭的插件不会增加路由、任务或资源。
- API 大数据集读取使用有界 cursor/过滤，条件请求正确，未改变现有客户端的 page/per-page 兼容路径。
- 归档可验证地保留内容、版本、媒体引用、重定向和允许的公开设置；秘密永不导出，失败不污染现有站点。
- 测试、race、vet、性能、迁移、隔离恢复、浏览器和无 JavaScript 验收均有实际记录；未执行项明确说明影响。
- 代码、文档和 Git 分批边界与本计划一致，主题插件仍保持独立。

