# 改进阶段三：10,000 篇文章性能深度优化方案

> 状态：已实施；应用层验收已完成，Compose 资源限制验收因当前 Docker daemon 权限未完成。
>
> 依据：[10,000 篇文章并发实测与瓶颈报告](../progress/phase-three-concurrency-load-report.md)
>
> 执行顺序与批次门禁：[阶段三性能优化实施计划](./phase-three-performance-implementation-plan.md)
>
> 范围：公开读取、分类/标签、文章冷渲染、页面缓存、搜索索引和 SQLite 读并发。
>
> 排除：主题插件、默认主题视觉回归、现有 `.playwright-results/` 运行产物和可选插件能力本身。

## 1. 优化目标与边界

### 1.1 目标

在不改变单站点、单 Owner、SQLite WAL、服务端渲染和 Markdown 权威模型的前提下，针对 10,000 篇已发布文章、单篇约 5,824 bytes 正文的数据集达到以下发布门槛：

| 指标 | 当前实测 | 优化目标 |
| --- | ---: | ---: |
| 首页/分类/标签单请求 | 首页约 30 s 后 500；分类/标签查询超过 10 s | 单请求成功；公开页面首次渲染 p95 ≤150 ms |
| 缓存命中公开页面 | 16 并发开始超过 25 ms；128 并发 p95 91–181 ms | 按 ADR-0031，在 100 RPS 短时压测中 p95 ≤25 ms、错误率 <0.1% |
| 不同文章冷缓存 | 32 并发 p95 18.87 s；64 并发出现 500 | 32 并发无 5xx，首次渲染 p95 ≤150 ms；64 并发至少有明确的受控降级而非渲染超时 500 |
| 中文搜索 | 已同步索引后服务调用约 2.43 s | 已完成索引时 p95 ≤250 ms；请求不执行全量同步 |
| 10,000 篇搜索索引重建 | 约 37–40 s，且可能与请求争用 Writer | 后台可恢复、分批、有进度；不阻塞公开请求；记录并持续优化重建耗时 |
| 内存与 SQLite | 默认 Reader=2；冷请求读等待很高 | 所有队列、缓存、连接和渲染并发有上限；不超过约 192 MiB Go heap soft limit |

“最大可能优化”不等于无限增加连接、缓存或 goroutine。最终容量必须在项目约 0.85 CPU/256 MiB 容器预算内，以目标环境的实测结果为准。

### 1.2 必须保持的不变量

- `content_revisions` 中的已发布快照仍是公开正文、标题、分类和标签身份的权威来源；草稿字段和编辑快照不能进入公开页面。
- 所有派生投影、正文 HTML、搜索索引和页面缓存均可从权威数据重建，不反向成为内容来源。
- 业务写事务保持短小；Markdown 渲染、HTML 清洗、媒体处理、索引大批量构建和网络操作不能进入发布事务。
- 发布、撤回、回收、公开 slug 或主题变化仍递增 `render_epoch`；缓存不能跨 epoch 错误复用。
- 公开 permalink、历史重定向、单站点/单 Owner 模型、CSRF/Origin/安全头和默认无第三方资源行为不变。
- 新增索引必须由真实查询和 `EXPLAIN QUERY PLAN` 证明有效；新增缓存、队列和连接池必须有大小上限。

## 2. 当前瓶颈判断

### P0：公共分类/标签查询形状错误

`PublicCategories` 对每个分类执行 3 个“最新文章”相关子查询；`PublicTags` 对 JSON 标签关系执行 `json_each`，并重复 3 个相关子查询。10,000 篇文章时，这种“每个术语重复扫描全部公开内容”的形状把首页、分类页、标签页和搜索页拖入超时。

### P0：公开缓存命中仍占用 SQLite Reader

`serveCachedDocument` 每次命中前都读取 `render_epoch`。页面缓存命中后虽然不再渲染正文，但仍会参与数据库读连接争用。单一的带 LRU 更新的缓存锁和响应体复制也会在高并发时放大延迟。

### P0：文章详情冷路径读取重复、渲染重复

文章详情先读取公开文章并解析公开 taxonomy，随后 `PublicArticleNavigation` 又按 ID 重新读取当前文章，再查询前后文章、相关文章并批量补 taxonomy。每个不同 permalink 都是独立 miss，现有 `RenderFlight` 不能合并不同文章的工作；Markdown 渲染、HTML 清洗和页面缓存同步落盘也在请求路径中。

### P1：Reader=2 在冷渲染时形成排队

Writer 等待为 0，但冷文章在 32/64/128 并发的 Reader 等待持续增加。Reader 从 2 提高可能降低排队，却不能修复分类/标签扫描，也可能在 0.85 CPU 下造成更多上下文切换，因此必须作为受控实验而不是盲目调大。

### P1：搜索请求承担索引同步

`SearchPage` 在执行查询前调用 `SyncAllDirty`。10,000 篇正文全量同步需要约 38 秒；即使没有脏数据，请求仍要走同步检查。搜索页还要先读取分类和标签选项，叠加了 P0 问题。

## 3. 总体技术路线

```text
观测与基线
    ↓
一次扫描的 taxonomy 查询 + 有证据的索引
    ↓
公开 taxonomy 派生投影 + epoch 共享缓存
    ↓
文章详情查询收敛 + 稳定 Markdown HTML 缓存 + 提交后预热
    ↓
移除请求内搜索同步 + 后台可恢复索引 + 搜索结果缓存
    ↓
Reader/渲染/页面缓存参数在目标资源预算内校准
    ↓
Docker 资源限制、100 RPS 和 10,000 篇完整验收
```

每一层都先保留旧路径作为回退，再用相同 fixture 和相同压测命令比较；未达到门槛时不扩大下一层范围。

## 4. 分阶段实施方案

### O0：建立可解释的性能基线

**目的**：把当前“请求很慢”拆成 SQL、Reader 等待、Markdown、主题模板、媒体、缓存落盘和响应写出几个时间段，避免优化错方向。

**实施内容**：

1. 给公开请求增加仅在诊断模式启用的分段计时：
   - epoch/state 读取；
   - 公开文章/卡片查询；
   - 公开 taxonomy 读取；
   - 文章导航和相关文章；
   - 媒体解析；
   - Markdown 转换、清洗和代码块增强；
   - 主题模板渲染；
   - PageCache 内存命中、磁盘读取和持久化；
   - Reader/Writer `sql.DB.Stats()` 快照。
2. 在压测脚本中增加按状态码、路由阶段、缓存状态和超时原因的统计，并区分“客户端超时”“应用 500”和“数据库错误”。
3. 增加 CPU、alloc、heap 和 goroutine profile 的离线入口；不把 pprof 暴露到公开路由，不把正文、令牌、绝对路径或敏感配置写入报告。
4. 扩充 fixture 分布：除单分类/单标签外，覆盖约 100 个分类、500 个标签、0/1/多个标签、不同正文长度、封面媒体和页面类型；保留原始 fixture 作为可比基线。

**验收**：同一 fixture 连续运行 3 次，报告中位数、p95、波动范围和 Reader 等待；所有后续阶段都使用同一套采样格式。

### O1：先修复分类/标签读取路径

#### O1.1 低风险方案：一次扫描、一次展开、批量返回

先不引入新的权威数据，重写现有只读查询：

- 分类：一次选出所有已发布文章和其发布修订，使用 CTE/window function 按分类分组，单次聚合计数，并从同一份排序结果取最新标题、路径和发布时间；删除 3 个相关子查询。
- 标签：先从已发布修订生成一次 `published_article_tags` 中间结果，再按标签聚合；每篇修订最多展开一次 `json_each`，不在三个相关子查询中重复展开。
- 分类页和标签页的 count、ID 分页查询复用同一投影形状，避免分类列表使用一套逻辑、摘要又使用另一套慢逻辑。
- 公开查询必须继续使用发布修订中的分类/标签快照，不能直接把 `contents.category_id` 或当前 `content_tags` 当成公开事实，否则草稿修改会泄露到公开页。

候选索引只在查询计划证明有效后加入下一次迁移，例如：

- 已发布内容过滤和发布时间排序的部分索引；
- `content_revisions.category_public_id` 的过滤辅助索引；
- 公开 taxonomy 派生表的 `(taxonomy_kind, taxonomy_public_id, published_at DESC, content_id DESC)` 索引。

索引方案必须比较写入成本、数据库文件增长和查询收益；不同时加入多个无法归因的宽索引。

#### O1.2 高收益方案：公开 taxonomy 派生投影

如果 O1.1 在完整 fixture 上仍不能稳定达到预算，增加由发布事实派生的投影表。建议采用一个带类型的成员表，逻辑字段如下：

```text
public_taxonomy_members
  content_id              INTEGER   -- 内部关联，不进入 URL
  published_revision_id   INTEGER
  taxonomy_kind           TEXT      -- category | tag
  taxonomy_public_id      BLOB
  title                   TEXT
  published_slug          TEXT
  published_at            INTEGER
```

设计规则：

- 一行代表一个“已发布文章—分类/标签”关系；标签 JSON 只在构建/更新投影时解析，公开读取不再展开 JSON。
- 投影中的标题、slug 和发布时间来自已发布修订/发布状态，避免读取草稿字段。
- 发布、撤回、回收、定时发布、公开 slug 变化时，只更新受影响文章的有限行；该更新不做 Markdown、网络或文件操作，可纳入原有短事务边界。
- 分类/标签名称和当前 slug 仍从 `categories`/`tags` 读取；术语改名不需要重建所有文章成员行。
- 旧数据通过可恢复、分批、带游标的 rebuild job 构建，绝不把 10,000 篇回填放进数据库迁移启动事务。
- 新代码读取投影前验证投影版本和完整性；回填期间回退到 O1.1 查询，验证完成后再切换读取路径。

投影表是缓存/派生数据，不得成为发布成功的唯一依据。投影更新失败必须进入可见、可重试的任务；公开读取始终有可靠的重建/回退路径。

#### O1.3 共享 taxonomy snapshot

增加按 `render_epoch` 版本化的共享公开 taxonomy snapshot：

- 分类摘要、标签摘要和搜索筛选选项共享同一份快照，避免首页、目录页和搜索页分别读取。
- 使用单 key render flight，多个并发 miss 只构建一次。
- 只保留当前 epoch 和一个旧 epoch，设置最大条目数/字节数；旧 epoch 仅用于正在结束的请求，不允许长期累积。
- 发布或 taxonomy 变更后由 epoch 自动失效；错误和超时不写入快照。

**O1 验收**：

- 10,000 篇完整 fixture 下公共分类和标签服务查询均成功；冷查询 p95 ≤150 ms，命中快照 p95 ≤25 ms。
- 首页、分类页、标签页和搜索页不再因 taxonomy 查询返回 500。
- 草稿修改不会改变公开 taxonomy；发布/撤回后 epoch、计数、最新文章和路径正确。
- `EXPLAIN QUERY PLAN` 和查询计时证明不再对每个术语重复扫描全部内容。

### O2：收敛文章详情冷路径

#### O2.1 删除重复公开文章读取

新增只读应用查询对象，例如 `PublicArticleView`，一次返回渲染所需的已发布正文、修订身份、公开 taxonomy 快照、slug、发布时间和封面公共 ID。文章导航接口改为接收已经读取的当前公开文章，禁止在 `PublicArticleNavigation` 内再次按 ID 查询当前文章。

导航查询调整为：

- 前一篇、后一篇使用已发布排序索引，各一次有界卡片投影；
- 相关文章通过 O1 taxonomy 投影查询，不再对每个候选文章执行 JSON 标签展开；
- 相关文章数量继续固定上限 3，并保留稳定排序；
- 导航卡片和正文文章共用一次媒体批量读取，避免单篇封面重复打开数据库。

每个公开文章请求应有明确的查询预算，目标是从当前的多次 Reader 查询降为“主体一次 + 有界导航/媒体批量查询”。

#### O2.2 稳定 Markdown HTML 派生缓存

当前 PageCache key 包含 `render_epoch` 和主题版本，发布或主题切换会使完整 HTML 重新渲染。增加独立的正文 HTML 派生缓存：

- key 至少包含 `published_revision_public_id`、Markdown 渲染器/清洗策略版本和代码块增强版本；不能只用 slug。
- 缓存值是经过 Goldmark、Bluemonday 和代码块增强后的安全 HTML，不允许存入未经清洗的 Markdown 或绕过 `html/template` 的通用 `safeHTML`。
- 主题切换和 render epoch 变化不使正文 HTML 无谓失效；文章新修订自然得到新 key。
- 内存 LRU、磁盘文件、单 key render flight 和磁盘清理均有独立上限；缓存损坏、版本不匹配或超限时删除并重建。
- 正文缓存仍然只是派生数据，不能替代数据库中的 Markdown。

这一步把最昂贵的 Markdown/HTML 工作从“每个公开页面 epoch miss 一次”降为“每个不可变发布修订一次”。

#### O2.3 发布后有界预热

发布事务提交后登记可重试任务，由任务执行器在事务外预热：

1. 当前文章详情和稳定正文 HTML；
2. 首页、第一页文章列表、受影响分类/标签页；
3. RSS、Sitemap 和搜索文档等已有派生任务按优先级执行。

预热任务必须：

- 有幂等键、租约、最大批次和失败重试；
- 不阻塞发布响应；
- 在文章撤回或再次编辑后检查 revision/epoch，过期任务安全跳过；
- 不为 10,000 篇文章创建无界任务。已有数据的全量预热使用带游标的 CLI/后台任务，可暂停、重启和限速。

#### O2.4 页面缓存写入与命中路径

在验证 cache consistency 后进行两项优化：

- 缓存命中路径减少单一 LRU 互斥锁竞争：评估读优化 map、分片 LRU 或异步 recency 更新；响应体只在必要时复制，禁止返回后续可修改的内部缓冲区。
- PageCache 先写有界内存条目并返回响应，磁盘持久化进入固定大小的 cache-writer 队列；队列满时丢弃磁盘副本但不丢响应，下一次仍可重建。磁盘文件继续使用临时文件、权限保护和原子 rename。

由于页面缓存可重建，磁盘 `Sync` 不应成为每个冷请求的同步尾延迟；但必须增加崩溃、重启和队列溢出测试，不能用异步写入掩盖缓存损坏。

**O2 验收**：

- 单篇 5,824 bytes 正文的冷详情在有稳定正文缓存/发布后预热时 p95 ≤150 ms。
- 文章详情不重复读取当前文章；数据库查询次数和各阶段耗时由诊断日志证明下降。
- 发布后首个访问即使预热任务尚未完成，也不会返回 500；任务失败后请求可重建缓存。
- 主题切换、发布、撤回和重启后缓存仍只展示正确的已发布版本。

### O3：移除搜索请求内的全量同步

#### O3.1 搜索索引改为后台派生

`SearchPage` 不再直接调用 `SyncAllDirty`。发布、撤回、分类/标签变更继续登记脏内容或版本化搜索任务，由后台按固定批次同步。搜索索引状态包含：

- 当前已构建的发布版本/epoch；
- pending 数量、最早 pending 时间和最近错误；
- 最近一次成功批次和耗时；
- rebuild 游标与 schema/tokenizer 版本。

搜索请求只读取已完成的索引，不等待全量 rebuild。产品语义采用“搜索派生数据最终一致”：新发布内容在索引任务完成前可能尚未出现在搜索结果，但公开文章页面、RSS 和 Sitemap 不受影响；后台必须明确显示 pending 状态。

如果业务必须保证单篇内容立即可搜索，只允许对该文章执行一个有界的增量同步，不得因一个请求触发 10,000 篇全量同步。

#### O3.2 索引构建缩短 Writer 占用

将索引流程拆为：

1. Reader 读取带 `published_revision_id` 的快照元数据和正文；
2. 事务外完成纯文本提取、中文 gram 生成和文档组装；
3. Writer 在短事务内批量替换仍匹配该 revision 的 `search_documents`、`search_grams` 和 dirty 状态；版本已变化的行重新入队。

这样避免把 Markdown/文本处理和大批量 gram 插入的准备工作放进一个长写事务。批次大小根据 WAL、锁等待和内存 profile 调整，默认必须有上限。

#### O3.3 搜索查询优化和缓存

- 中文 gram 计数和结果查询使用稳定的复合索引和一次性候选集，减少重复 `GROUP BY`/排序；先用 `EXPLAIN` 证明，再考虑 SQL 形状调整。
- 评估把计数和结果合并为一个有界查询，或对第一页使用“是否有下一页”而不是精确全量 count；若保留精确分页，需把 count 的耗时单独纳入预算。
- 增加按 `render_epoch + 规范化 query + kind/category/tag/sort/page` 的有界搜索结果缓存；不缓存未授权或含用户状态的结果。
- 搜索缓存只存结果 ID/公开摘要和有限响应大小，最大条目数、TTL 和总字节数固定；新发布 epoch 自动失效。
- `/search` 的分类/标签筛选选项从 O1 snapshot 读取，不能再阻塞在慢查询上。

**O3 验收**：

- 已完成索引的中文搜索 p95 ≤250 ms；搜索请求不执行 Writer 同步事务。
- 全量 rebuild 可暂停、重启、重试，失败不会损坏已有可用索引。
- 10,000 篇长正文重建期间，公开文章和缓存命中请求仍满足各自延迟/错误预算。

### O4：Reader、渲染并发和资源校准

#### O4.1 Reader 连接数实验

在 O1–O3 完成后，固定代码分别测试 `read_connections=2/4/6/8`：

- 同一数据集、同一请求矩阵、同一 CPU/内存限制；
- 比较 p50/p95/p99、Reader WaitCount/WaitDuration、Writer 等待、CPU、heap、WAL 大小和错误率；
- 只选择满足延迟且没有持续内存/CPU增长的最小配置；默认不直接改为 8。

推荐决策规则：若 Reader 等待下降但 CPU 饱和、p95 不降或内存超预算，回退到较小连接池；连接池不能替代慢 SQL。

#### O4.2 有界公共渲染门

如果正文缓存和预热后仍出现大量不同文章同时 miss，增加有界的公共渲染门，保护 CPU、heap 和磁盘：

- leader 渲染总数固定上限，等待者只等待同 key 的 render flight；不同 key 不得无限排队。
- 排队等待接近请求 deadline 时返回明确的可重试状态和 `Retry-After`，避免把内部超时伪装成 500。
- 任务、等待、绕过和超时计数进入诊断指标。

渲染门是过载保护，不把它当成提升单机吞吐的手段；只有在 O2 缓存优化后仍有资源尖峰时才启用。

#### O4.3 进程内公开状态与缓存锁

当前缓存命中每次查询 `render_epoch`。设计一个单进程、原子可读的公开 render state：

- 启动从 SQLite 加载权威 epoch；
- 所有递增 epoch 的成功写路径通过统一状态写入边界通知进程内 state；
- state 只作为快速读取缓存，SQLite 仍是重启后的权威来源；通知丢失或进程重启时重新加载；
- 测试覆盖发布、撤回、slug、分类/标签、导航、主题设置等所有公开输出写路径，确保不会提供旧 epoch 页面。

这样缓存命中不必为每个请求占用一个 SQLite Reader，预期同时降低高并发 cache-hit 的延迟和 Reader WaitCount。

## 5. 数据库迁移、任务与回滚

### 5.1 迁移策略

- 若 O1.1 已满足目标，只提交必要的查询索引迁移；不创建未使用的 projection 表。
- 若需要 O1.2，新增下一编号迁移（预计 `00017`），只创建表、约束和索引，不在迁移事务中回填 10,000 篇数据。
- 新迁移必须覆盖空库、升级库、重复启动和中途失败；已有迁移不修改、不重排。
- Projection rebuild 使用任务/CLI 批次执行，保存 cursor、schema version、开始/完成时间和错误摘要。

### 5.2 发布和派生任务

- 发布事务只写受影响的有限 projection 行、render epoch 和事件/任务登记；不执行 Markdown、网络、全量 FTS 或磁盘预热。
- 任务按优先级处理：公开文章正文/页面预热 > taxonomy projection 修复 > 搜索索引 > RSS/Sitemap 等低优先级派生；每一类有独立最大并发。
- 任务必须幂等；重复消费不能产生重复成员、重复缓存或重复搜索行。
- projection/search/cache 任一派生失败不回滚已成功的发布，但后台必须显示失败和可重试状态。

### 5.3 回滚

- 查询优化和缓存通过代码路径开关或版本化接口切换；失败时回退旧查询，保留新增派生表和数据以便诊断。
- 不在生产环境对已应用迁移执行破坏性 down；回滚代码后旧版本忽略新增派生表即可。
- 如果正文 HTML 或页面缓存格式改变，使用 renderer/schema version 生成新 key，禁止解释旧格式为新格式。
- 搜索索引损坏时保留正文和已发布状态，使用后台 rebuild 恢复；搜索暂不可用不能影响文章公开页。

## 6. 测试与验收矩阵

### 6.1 单元与集成测试

- `organization`：分类/标签聚合结果、最新文章排序、草稿与发布快照隔离、空分类/空标签、多个标签、重命名、删除/撤回和 projection rebuild 幂等。
- `publishing`：详情导航不重复读取当前文章、发布/撤回/定时发布同步 projection、slug 重定向和 epoch 正确递增。
- `presentation`：正文 HTML 缓存 key、清洗策略版本、主题切换复用正文缓存、epoch 不串页、缓存损坏/队列满/进程重启安全。
- `discovery`：请求不触发全量同步、后台索引版本、失败重试、重复任务、中文 gram 查询、搜索结果缓存失效。
- `database`：新增索引/投影迁移空库、升级库、失败重启、WAL 和连接池配置。

### 6.2 10,000 篇 HTTP 验收

使用固定长正文和代表性分类/标签分布，至少执行：

1. 首页、分类目录、标签目录、搜索、单篇文章的单请求冷启动和重复请求；
2. 1/2/4/8/16/32/64/128 并发的缓存命中矩阵；
3. 32/64/128 个不同文章 permalink 的冷缓存矩阵；
4. 发布、撤回、主题版本变化后的缓存失效和预热；
5. 搜索 rebuild 与公开读取并行；
6. Reader=2/4/6/8 对照实验；
7. 进程重启、任务租约恢复、缓存清理和 projection rebuild 恢复。

每轮记录 p50/p95/p99、错误率、状态码分布、Reader/Writer 等待、CPU、heap、GC、goroutine、WAL 和缓存命中率。

### 6.3 发布门槛

必须同时满足：

- 首页、分类、标签和搜索单请求无 5xx；
- 缓存命中满足 ADR-0031 的 25 ms p95 和 100 RPS 错误率 <0.1%；
- 首次公开渲染 p95 ≤150 ms，或明确记录未满足项并阻止将其标记为阶段完成；
- 搜索已完成索引时 p95 ≤250 ms，搜索请求不持有长 Writer 事务；
- 读/写连接、渲染门、任务、缓存和 projection 均有固定上限；
- 10,000 篇数据下 heap 不持续增长，未超过约 192 MiB Go heap soft limit；
- `go test -tags 'fts5 sqlite_omit_load_extension' ./...`、`make test-race`、`make vet`、`make perf-gate`、严格浏览器回归和目标环境 HTTP 压测均通过；
- Docker 资源限制下完成 `/livez`、`/readyz`、首页和公开文章健康检查。

## 7. 建议的提交批次

实施时按模块拆分，便于回滚和归因：

1. `perf: add public read phase metrics and representative 10k fixture`
2. `perf: rewrite public taxonomy queries and add evidence-backed indexes`
3. `perf: add versioned public taxonomy projection and rebuild task`（仅当 O1.1 不足时）
4. `perf: collapse public article reads and navigation queries`
5. `perf: add stable markdown render cache and bounded warmup`
6. `perf: optimize page cache hit and asynchronous persistence`
7. `perf: move search synchronization to durable background work`
8. `perf: calibrate reader pool and public render limits`
9. `test: add 10k HTTP acceptance and resource-budget gates`
10. `docs: record post-optimization capacity and residual risks`

每个提交都应包含对应测试和 `git diff --check`；主题插件相关路径继续独立排除，不与本方案混合提交。

## 8. 暂不采用的方案

- 不引入 Redis、Elasticsearch、消息队列或独立搜索服务来掩盖当前 SQL/缓存问题；它们会改变部署边界，只有 SQLite 单机优化证据充分且容量目标仍不满足时才另行评估。
- 不把所有文章 HTML 预先存入权威数据库；这会引入第二正文来源和大事务风险。预渲染只作为可重建缓存/任务产物。
- 不把 Reader 连接数无限调高，不把 PageCache、搜索缓存或 render flight 改成无界 map。
- 不在公开读取中使用当前草稿 taxonomy、跳过 Markdown 清洗、放宽模板安全边界或通过增加超时掩盖慢查询。
- 不通过关闭页面缓存持久化、关闭 WAL 或放宽数据一致性来换取未验证的吞吐提升。
