# 改进阶段三：性能优化实施计划

> 状态：已实施；本地工程门禁通过，Compose 资源压测受 Docker daemon 权限限制未完成。
>
> 本文是[阶段三性能深度优化方案](./phase-three-performance-optimization-plan.md)的执行计划，依据[10,000 篇文章并发实测与瓶颈报告](../progress/phase-three-concurrency-load-report.md)编写。
>
> 本轮只规划公开读取、分类/标签、文章冷渲染、页面缓存、搜索索引和 SQLite 读并发优化；主题插件、默认主题视觉回归、已有运行产物和可选插件不在本计划内。

实际实施与验收证据见[阶段三性能实施与验收报告](../progress/phase-three-performance-implementation.md)。原始优化前基线报告保持不变，优化后结果单独记录，避免把基线改写成优化结论。

## 1. 实施结果

本计划实际交付以下结果：

1. 一套可重复的 10,000 篇文章性能基线、分段耗时和资源报告。
2. 分类、标签、首页和搜索筛选读取不再因重复 SQL 扫描超时。
3. 文章详情冷路径不重复读取当前文章；Markdown HTML、页面缓存和发布后预热均为有界的可重建派生数据。
4. 搜索请求不再同步执行全量 dirty 索引；索引重建可分批、可恢复、可观察。
5. 完成 Reader=2/4/8 对照、渲染并发、缓存和任务队列的应用层校准；项目资源预算下的最终部署值待 Compose 资源压测。
6. 同一套代表性 10,000 篇文章压测能够给出优化后的 p50/p95/p99、错误率、Reader/Writer 等待和堆结果，并明确剩余瓶颈。

不以“增加超时、无限增大连接池、关闭一致性检查或暂时不返回错误”作为完成条件。

## 2. 约束与实施原则

### 2.1 当前工作区边界

- 开始每个实施批次前先执行 `git status --short`，保留当前阶段三规模与运维改动、未跟踪测试、主题目录和 `.playwright-results/`；不使用 reset、checkout 或清理命令覆盖它们。
- 当前工作区已有 `db/migrations/00016_phase3_scale_operations.sql` 未提交文件。性能投影迁移必须在确认最新迁移序号后使用下一个空闲编号，不能预先假定编号或修改已有迁移。
- 性能优化代码与主题插件保持独立；不修改 `themes/cel-panel/`、主题专用测试和主题运行产物。
- 本文记录实施顺序、门禁和回滚边界；实际代码、压测输出和未完成项单独记录在[实施与验收报告](../progress/phase-three-performance-implementation.md)。压测使用临时数据库，不创建或覆盖项目运行数据库、备份和主题产物。

### 2.2 不可改变的不变量

- 公开内容只来自 `content_revisions` 的已发布快照；草稿、编辑快照和当前编辑字段不能进入公开投影。
- Markdown、已发布修订和永久链接仍是权威数据；taxonomy 投影、正文 HTML、搜索索引和页面缓存都必须可以删除后重建。
- 发布、撤回、slug、分类/标签、站点设置和主题变化继续递增 `render_epoch`。
- 业务写事务保持短小。Markdown 渲染、HTML 清洗、中文分词、网络访问和磁盘大批量操作不能放进发布事务。
- 所有连接池、缓存、flight、任务、预热和重建批次都有固定上限。
- 页面和搜索仍保持服务端渲染、默认无第三方资源、现有安全清洗和缓存隔离语义。

### 2.3 固定验证环境

Go 命令统一使用项目要求的构建标签：

```bash
go test -tags 'fts5 sqlite_omit_load_extension' ./...
```

每次性能比较固定以下变量，除非正在执行 Reader 参数实验：

- 10,000 篇已发布文章；保留现有 5,824 bytes 正文 fixture，并新增分类、标签、正文长度和媒体分布 fixture。
- SQLite WAL、Writer=1、Reader=2，应用和测试端均使用同一份 fixture。
- 同一组路由、请求数量、并发档位、超时和结果统计格式。
- 关键门禁至少重复三次，报告中位数、p95、波动范围和状态码分布；未完成三轮或存在资源环境差异时，不以单次最好结果作为门槛证据。

## 3. 批次总览与依赖

```text
B0 基线与观测
        ↓
B1 taxonomy 查询重写 ──────┐
        ↓                   │
B2 taxonomy 投影（条件）   │
        └──────────────┬────┘
                       ↓
B3 文章详情与正文缓存
                       ↓
B4 页面缓存命中与预热
                       ↓
B5 搜索后台化与批处理
                       ↓
B6 Reader/渲染资源校准
                       ↓
B7 完整验收与容量记录
```

| 批次 | 目标 | 是否必须 | 主要模块 | 出口条件 |
| --- | --- | --- | --- | --- |
| B0 | 建立可解释的基线 | 必须 | `internal/app`、`internal/perf`、`internal/platform/database` | 能区分 SQL、Reader 等待、渲染和缓存写入耗时 |
| B1 | 修复分类/标签查询形状 | 必须 | `internal/organization`、数据库迁移 | 首页、分类、标签不再超时或 500 |
| B2 | 增加 taxonomy 派生投影 | 条件 | `internal/organization`、`internal/operations`、发布事件 | B1 未达到目标，且投影回填可恢复 |
| B3 | 收敛文章冷路径 | 必须 | `internal/publishing`、`internal/presentation` | 详情查询无重复主体读取，正文 HTML 有稳定版本 key |
| B4 | 降低缓存命中争用并预热 | 必须 | `internal/presentation`、`internal/app` | 预热不阻塞发布，缓存写盘不阻塞响应 |
| B5 | 将搜索同步移出请求 | 必须 | `internal/discovery`、`internal/extensions`、`internal/app` | 搜索请求无全量 Writer 同步，索引任务可恢复 |
| B6 | 校准资源参数 | 必须 | `internal/platform/config`、数据库、presentation | 选择满足预算的最小 Reader/渲染配置 |
| B7 | 完整验收与记录 | 必须 | `internal/perf`、`docs/progress`、脚本 | 形成优化后容量结论和残余风险 |

每个批次完成后先通过本批次出口条件，再进入下一批次。任何 P0 查询或一致性测试未通过时，停止扩展缓存和并发优化范围。

## 4. 详细实施步骤

### B0：基线、分段观测与测试夹具

**目的**：在改变查询前确认时间到底消耗在 SQL、连接池、Markdown、模板还是缓存持久化。

**工作项**：

1. 在诊断路径增加阶段计时和计数：epoch/state、公开主体查询、taxonomy、导航/相关文章、媒体、Markdown、主题模板、PageCache 命中/落盘和响应输出。
2. 为数据库 Reader/Writer 记录 `sql.DB.Stats()` 快照，至少包含等待次数、等待时间、打开连接和空闲连接；不记录正文、令牌、路径中的敏感值或完整请求参数。
3. 扩展 `internal/app/phase3_http_load_test.go` 的结果统计，区分应用 500、客户端超时、数据库错误、缓存 hit/miss 和渲染 deadline。
4. 为 1,000 和 10,000 篇文章生成可重复 fixture，覆盖 0/1/多标签、分类数量、长短正文、封面媒体、首页、归档、搜索和详情。
5. 为分类、标签、搜索和详情查询生成 `EXPLAIN QUERY PLAN` 记录；报告中同时保存查询数量、返回行数和耗时。

**候选文件**：`internal/app/phase3_http_load_test.go`、`internal/perf/`、`internal/platform/database/`、`internal/presentation/http.go`、相关模块测试。

**验收证据**：连续三次基线报告；每个关键路由有 p50/p95、状态码、Reader/Writer 等待和内存结果；当前首页 500、分类/标签超时和冷文章延迟均能复现或明确说明环境差异。

### B1：分类/标签查询重写与索引证据

**目的**：先用低风险 SQL 改造解决最大 P0，不立即引入新投影表。

**工作项**：

1. 在 `internal/organization/repository.go` 重写 `PublicCategories`：已发布文章/修订只扫描一次，用 CTE/window 或等价聚合得到计数、最新标题、路径和时间，删除按分类重复执行的最新文章子查询。
2. 重写 `PublicTags`：每个已发布修订只展开一次标签 JSON，再聚合计数和最新文章摘要；不在多个相关子查询中重复 `json_each`。
3. 复用同一公开快照语义改造 `PublicCategoryPage`、`PublicTagPage` 的 count 和分页 ID 查询；不得切换到当前草稿 taxonomy。
4. 通过 `EXPLAIN QUERY PLAN` 和 1,000/10,000 篇数据比较索引收益，只为真实过滤、连接和排序增加最小索引。索引不能与查询一起无证据批量加入。
5. 增加空分类、空标签、多标签、草稿修改、发布/撤回、分类重命名和最新文章排序测试。

**候选文件**：`internal/organization/repository.go`、`internal/organization/service_test.go`、新增或补充 organization repository 集成测试、下一编号数据库迁移（仅在有证据时）。

**出口门槛**：

- 10,000 篇 fixture 下分类/标签服务查询成功，单请求 p95 ≤150 ms；首页、分类、标签和搜索页面不返回由 taxonomy 引起的 500。
- 查询计划不再对每个术语重复扫描全部已发布内容；公开字段与旧路径在完整 fixture 上逐项一致。
- 若 B1 达标，跳过 B2；若不达标，保留 B1 作为 fallback，进入 B2。

### B2：taxonomy 派生投影（仅在 B1 不足时实施）

**触发条件**：B1 在固定环境连续三次未达到单请求目标，或查询计划仍显示随术语数量扩大的重复扫描。

**工作项**：

1. 在下一空闲迁移中创建 `public_taxonomy_members`，至少包含 `content_id`、`published_revision_id`、`taxonomy_kind`、`taxonomy_public_id`、`title`、`published_slug` 和 `published_at`，并增加经过查询计划证明有效的组合索引。
2. 明确投影所有权和接口：公开读取通过 organization 的公开查询接口；publishing 只通过已发布事件/应用服务通知受影响内容，不直接访问 organization 私有表。
3. 为发布、撤回、定时发布、slug 变化和分类/标签快照变化增加有限行更新；失败进入持久任务，不回滚已成功的发布。
4. 增加带 cursor 的 rebuild 任务，记录 schema version、cursor、批大小、状态、最近错误和完成时间；迁移只建表和索引，不回填 10,000 篇文章。
5. 回填未完成或投影校验失败时回退 B1 查询；投影完成后通过一致性计数和随机样本校验，再切换读取路径。
6. 对分类摘要、标签摘要和搜索筛选项增加按 `render_epoch` 的共享 snapshot；只保留当前和一个旧 epoch，设置最大字节数和 singleflight。

**候选文件**：`db/migrations/`、`internal/organization/model.go`、`internal/organization/repository.go`、`internal/organization/service.go`、`internal/extensions/host.go` 或 core task 注册处、`internal/app/app.go`、organization 测试和性能测试。

**出口门槛**：空库、升级库、重复 rebuild、中断恢复和投影损坏 fallback 均通过；发布与撤回后的 taxonomy、slug 和 epoch 正确；公开读取不依赖草稿字段。

### B3：文章详情查询收敛与稳定正文缓存

**目的**：减少详情冷路径的主体重复读取，将 Goldmark/Bluemonday 处理按不可变发布版本复用。

**工作项**：

1. 在 `internal/publishing` 增加公开文章读取选择器/视图模型，一次返回正文、已发布修订 ID、slug、时间、taxonomy 和封面公共信息。
2. 将 `PublicArticleNavigation` 改为接收已读取的当前文章或其最小只读视图，删除内部再次调用 `PublicContentByID` 的路径。
3. 使用有界的前后文章查询和 taxonomy 投影相关文章查询，相关文章最多 3 条并保持稳定排序；媒体使用批量读取。
4. 在 `internal/presentation` 增加正文 HTML 派生缓存，key 至少包含 `published_revision_public_id`、Markdown renderer version、sanitizer version 和 code-block enhancer version。
5. 正文缓存继续经过既有 Markdown 解析、清洗和代码块增强；缓存损坏、版本不匹配、超过大小或读取失败时删除并重建。
6. 为发布后预热设计幂等任务：先预热当前文章详情和正文 HTML，再按上限预热首页、第一页列表及受影响 taxonomy 页；任务检查 revision/epoch，过期任务跳过。

**候选文件**：`internal/publishing/model.go`、`internal/publishing/repository.go`、`internal/publishing/service.go`、`internal/publishing/service_test.go`、`internal/presentation/markdown.go`、`internal/presentation/cache.go`、`internal/presentation/http.go`、`internal/app/app.go`。

**出口门槛**：

- 详情请求不重复读取当前文章，查询次数和阶段耗时有测试或诊断证据。
- 5,824 bytes 正文冷详情在预热/正文缓存有效时 p95 ≤150 ms；预热失败时请求仍能同步重建且不返回 500。
- 主题版本变化可以复用稳定正文 HTML，但完整页面仍按主题和 epoch 正确失效；缓存不跨发布修订串用。

### B4：页面缓存命中、epoch 状态和异步持久化

**目的**：减少缓存命中路径的 Reader 争用，并移除页面缓存同步落盘造成的请求尾延迟。

**工作项**：

1. 在 `internal/presentation/http.go` 和相关状态组件中设计进程内原子 render state：启动从 SQLite 加载，所有公开输出写路径在提交成功后通知，SQLite 仍是重启后的权威来源。
2. 盘点 publishing、organization、presentation/theme 等所有 `render_epoch` 递增路径，逐一接入通知并增加失效测试；通知失败不得让旧 epoch 被当成新 epoch 使用。
3. 优化 `PageCache` 的内存命中锁竞争，评估分片 LRU或受控的 recency 更新；响应不得暴露可被后续写入的内部 byte slice。
4. 页面先写入有界内存条目并完成响应，磁盘持久化进入固定大小的 cache-writer 队列；队列满时允许丢弃磁盘副本，不阻塞公开响应。
5. 保留临时文件、权限、原子 rename、大小上限和清理机制；增加进程重启、半写文件、队列满、磁盘满和 epoch 变化测试。

**候选文件**：`internal/presentation/cache.go`、`internal/presentation/http.go`、`internal/presentation/theme_catalog.go`、`internal/publishing/lifecycle.go`、`internal/publishing/repository.go`、`internal/organization/repository.go`、`internal/app/app.go`。

**出口门槛**：缓存 hit 不再为每次请求读取 SQLite epoch；100 RPS 短压的 p95 ≤25 ms 且错误率 <0.1%；异步磁盘写入失败不影响响应，重启后缓存可安全重建。

### B5：搜索请求去同步与后台批处理

**目的**：将 10,000 篇索引的约 38 秒成本从搜索请求中移出，并缩短 Writer 事务。

**工作项**：

1. 在 `internal/discovery/service.go` 移除 `SearchPage` 对 `SyncAllDirty` 的请求内调用；搜索只读取已完成的派生索引。
2. 在发布、撤回和 taxonomy 变更后继续登记 dirty/version 信息，并通过现有持久任务/事件边界安排有界搜索同步；任务 key 和 revision 必须幂等。
3. 将 `SyncDirty` 拆为 Reader 快照读取、事务外纯文本/中文 gram 构建、短 Writer 批量替换和 revision 校验；不在长 Writer 事务中做文本准备和逐行大批量计算。
4. 为全量 rebuild 增加可暂停、可恢复 cursor、批次上限、进度和最近错误；已有索引在 rebuild 失败时保持可用。
5. 用 `EXPLAIN QUERY PLAN` 优化 `search_grams` 的候选、count、排序和分页；评估第一页是否需要精确 count，但不能未经确认改变已有分页语义。
6. 增加按 `render_epoch + normalized query + filters + sort + page` 的有界搜索结果缓存；不缓存授权、用户状态或未完成索引状态相关结果。
7. 明确最终一致语义：发布成功不被搜索索引失败回滚，后台页面显示 pending/failed；若产品要求单篇立即可搜，只执行有界的单文章同步。

**候选文件**：`internal/discovery/service.go`、`internal/discovery/repository.go`、`internal/discovery/model.go`、`internal/discovery/service_test.go`、`internal/extensions/host.go`、`internal/app/app.go`、publishing 事件/任务接入处。

**出口门槛**：已完成索引的搜索 p95 ≤250 ms；搜索请求不触发全量 Writer 同步；10,000 篇 rebuild 可重启并继续，期间公开文章和缓存命中不受长写事务阻塞。

### B6：Reader、渲染门和内存参数校准

**目的**：在慢 SQL 和冷渲染重复工作消除后，选择资源预算内的最小并发参数。

**工作项**：

1. 固定代码和 fixture，分别测试 `read_connections=2/4/6/8`；记录 p50/p95/p99、CPU、heap、GC、goroutine、WAL、Reader/Writer 等待和错误率。
2. 仅当正文缓存、页面缓存和预热完成后仍出现不同文章同时 miss，才启用有界公共 render gate；同 key 继续使用 RenderFlight 合并，不同 key 不得无限排队。
3. 将接近 deadline 的渲染排队转为明确的受控降级/重试语义，不能把内部饥饿全部表现为 500；记录 leader、coalesced、rejected 和 timeout 计数。
4. 选择满足预算且无持续内存增长的最小 Reader 和 render 配置；不因等待次数下降就自动选择 8 个 Reader。

**出口门槛**：在 0.85 CPU/256 MiB 目标约束下，Go heap 不持续增长且不超过约 192 MiB soft limit；冷文章 32 并发无 5xx，并明确 64/128 并发的受控降级行为。

### B7：完整验收与容量记录

**工作项**：

1. 重跑缓存命中、不同文章冷缓存、首页/分类/标签、搜索、重建并行、发布/撤回失效、重启恢复和 Reader 对照矩阵。
2. 在可用环境中使用 Compose 资源限制重跑；宿主机上的 httptest 结果只作为应用层证据，不直接宣称生产 RPS。
3. 将实际配置、迁移版本、每批次提交、门禁结果、失败样本、未运行检查和残余风险写入 `docs/progress/phase-three-performance-implementation.md`。
4. 若性能仍不达标，记录新的最大瓶颈和下一步边界；不得把未达标的实验结果改写成阶段完成。

## 5. 数据库与任务实施规则

### 5.1 迁移

- 查询优化没有证据时不加索引；索引迁移必须包含 `EXPLAIN QUERY PLAN` 前后对照和文件增长记录。
- taxonomy 投影迁移只建表、约束和索引，不在应用启动或 Goose 迁移事务中回填数据。
- 空库、从现有最新迁移升级、重复启动和迁移中断重启均需验证；生产不使用 Goose Down 做回滚。
- 迁移失败不能破坏 `content_revisions`、已发布状态或已有可用搜索索引。

### 5.2 任务

- 任务使用现有 `jobs` 的状态、租约、最大尝试次数、退避和幂等键；payload 只保存小型 ID、revision、cursor 和版本，不放正文。
- 每个批次先 claim，再在事务外执行 Markdown/文本/文件操作，最后以短事务完成状态更新。
- taxonomy rebuild、搜索 rebuild、正文预热和页面预热分开计数并限制并发，避免一个全量任务占满共享队列。
- 失败任务保留脱敏错误摘要；重复任务不能产生重复投影、重复缓存或重复搜索行。

### 5.3 一致性开关

优先使用构造函数选项、版本化 key 和可回退查询实现灰度，而不是增加公开配置项。若必须引入运行时开关，应满足：默认关闭新路径、启动时校验、后台可见、失败可回退、不会绕过权限或安全清洗。

搜索改为后台最终一致属于公开行为语义变化；实施 B5 前应补充或更新对应 ADR，并在管理端明确 pending/failed 状态。

## 6. 测试与验收命令

### 6.1 聚焦测试

```bash
go test -tags 'fts5 sqlite_omit_load_extension' ./internal/organization ./internal/publishing ./internal/presentation ./internal/discovery
go test -tags 'fts5 sqlite_omit_load_extension' ./internal/platform/database ./internal/operations ./internal/extensions
```

每个批次还要运行对应包的 race 测试；PageCache、RenderFlight、任务租约、投影 rebuild 和缓存失效必须覆盖并发、失败和重启路径。

### 6.2 10,000 篇 HTTP 基线与回归

缓存和冷文章命令沿用报告中的入口，保持环境变量不变：

```bash
PHASE3_LOAD_REQUESTS=256 PHASE3_LOAD_COLD_REQUESTS=32 \
go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app \
  -run TestPhase3HTTPConcurrency -count=1 -v
```

```bash
PHASE3_LOAD_LEVELS=32,64,128 \
PHASE3_LOAD_SCENARIOS=cold-article \
PHASE3_LOAD_COLD_REQUESTS=128 \
PHASE3_LOAD_SKIP_PROBES=1 \
go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app \
  -run TestPhase3HTTPConcurrency -count=1 -v
```

### 6.3 工程门禁

完成所有代码批次后执行：

```bash
make test
make test-race
make vet
make perf-gate
make stage3-acceptance
git diff --check
```

如果 Docker、Playwright、Go profile 或 Compose 资源限制不可用，进度文档必须记录具体未运行项和影响，不能用本地单进程结果替代。

## 7. 回滚与停止条件

### 7.1 停止推进

满足以下任一条件，停止进入下一批次并回到当前批次修复：

- 公开字段、已发布快照、slug 重定向或 epoch 与旧路径不一致。
- 分类、标签、首页或搜索出现 5xx、草稿泄露或未定义的空结果。
- Reader 等待降低但 CPU、heap、WAL 或 p95 恶化。
- 任务重复执行造成重复成员、脏状态丢失或搜索索引不可重建。
- 缓存命中返回旧主题/旧 epoch/旧 revision，或异步写入导致损坏文件被当作有效缓存。

### 7.2 回退方式

- B1 查询保留旧实现的测试和 fallback；新索引只影响计划中的查询，不能改变数据语义。
- B2 投影读取失败回退 B1；不删除权威发布数据，不在生产执行破坏性 down migration。
- B3/B4 使用新的 renderer/cache schema version 生成新 key；旧缓存不强行按新格式解析。
- B5 搜索同步失败保留正文和已发布状态，停用新同步读取路径后从 dirty 数据重建；搜索暂时不可用不能影响公开文章页。
- B6 连接数、render gate 和队列容量回退到上一个已验证配置；不保留无界 fallback。

## 8. Git 分批与文档产物

本计划不创建提交。实际实施时按模块和可回滚边界分批，建议顺序如下：

1. `docs(phase3): add performance implementation plan`
2. `perf: add public read phases and deterministic 10k fixture`
3. `perf(organization): rewrite public taxonomy queries`
4. `perf(organization): add public taxonomy projection and rebuild`（仅 B2 触发时）
5. `perf(publishing): collapse public article reads and navigation`
6. `perf(presentation): add versioned markdown and page cache paths`
7. `perf(discovery): move search synchronization to bounded background work`
8. `perf(platform): calibrate reader and render resource limits`
9. `test(phase3): add performance and recovery acceptance gates`
10. `docs(phase3): record optimized capacity and residual risks`

每个代码提交必须包含对应失败路径测试，并通过 `gofmt`、`git diff --check` 和该批次聚焦测试。主题插件路径、主题专用测试和运行产物不混入以上提交。

最终文档产物：

- 本文：实施顺序、模块边界、门禁和回滚规则。
- `phase-three-performance-optimization-plan.md`：技术方案和目标设计。
- `docs/progress/phase-three-performance-implementation.md`：实际实施证据、提交、配置、容量和剩余风险。
- `phase-three-concurrency-load-report.md`：优化前固定基线，不覆盖、不改写为优化后结论。

## 9. 完成判定

只有同时满足以下条件，才能将性能优化阶段标记为完成：

- 首页、分类、标签和搜索单请求无 5xx；缓存命中 p95 ≤25 ms，100 RPS 错误率 <0.1%。
- 首次公开渲染 p95 ≤150 ms；已完成索引的搜索 p95 ≤250 ms。
- 10,000 篇文章下冷文章 32 并发无 5xx，64/128 并发有明确、可观测且不破坏数据的受控降级。
- taxonomy、正文 HTML、搜索索引和页面缓存均可从权威数据重建；发布/撤回/主题切换/重启后没有旧内容串出。
- 连接池、缓存、队列、render gate 和 rebuild 批次均有固定上限，Go heap 不持续增长且符合项目资源预算。
- 相关 Go 测试、race、vet、perf-gate、stage3 acceptance、目标环境压测和可用的浏览器/Compose 检查均有实际记录；未运行项已明确列出。
- 已提交实际容量与残余风险记录；未因主题插件或单次最好成绩提前宣布完成。
