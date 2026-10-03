# 阶段三性能优化实施与验收报告

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

> 实测日期：2026-09-05（UTC）；阶段四后续代码已继续保持公开列表轻量投影和有界媒体批量读取，本报告的阶段三数字不回写。
>
> 本报告只记录当前工作区代码的实际结果。优化前基线保留在[阶段三并发实测与瓶颈报告](./phase-three-concurrency-load-report.md)，没有用优化后数字覆盖基线。

## 1. 结论

本轮优化已经通过应用层功能、race、vet、性能门禁和 10,000 篇文章 HTTP 压测。最新完整矩阵中所有公开请求均返回 200，没有应用错误、状态码失败或 Writer 等待。

在搜索页完成一次预热后，固定目标 100 RPS 的真实 HTTP 压测结果为：文章缓存路径完成 98.4 RPS、p95 18.138 ms；搜索路径完成 99.1 RPS、p95 180.508 ms；两者均 256/256 返回 200，Reader/Writer 等待均为 0。这个结果代表 steady-state，不代表冷启动首个搜索 miss 的延迟；搜索 p95 受当前宿主机调度抖动影响，未稳定达到 25 ms 目标。

当前仍有两项不能宣称“生产验收通过”的证据缺口：

1. 搜索 steady-state 的单轮 p95 仍可能受宿主机调度放大；本次预热 c8/目标 100 RPS 为 180.508 ms。未预热的同一场景本轮 p95 为 69.210 ms、p99 为 475.082 ms。搜索 HTML 快照能显著降低首个 miss 后的重复成本，但不能把不稳定的宿主机资源当成生产保证。
2. Docker CLI/Compose 已安装，但当前账号无权访问 `/var/run/docker.sock`，未能在 0.85 CPU/256 MiB 容器资源约束下启动服务并测量 RSS、Go heap、GC、WAL 和真实 Caddy 链路。因此本报告不把宿主机 `httptest` 结果写成生产容量结论。

## 2. 已实施内容

- 新增 `00017_phase3_public_taxonomy.sql`，建立可重建的 `public_taxonomy_members` 投影和有状态、分批、可恢复的 rebuild；发布、撤回、定时发布维护受影响投影行，未就绪时回退查询路径。
- 新增 `00018_phase3_admin_content_index.sql`，为后台内容分页和计数增加 `(kind, trashed_at, updated_at, id)` 复合索引；10,000 篇规模专测 admin 查询为 11.896 ms，后续性能门禁同项为 23.369 ms，均远低于 2 s 生产门槛。
- 重写分类/标签公开查询，增加覆盖索引证据和按 `render_epoch` 的有界摘要缓存；分类与标签共享 epoch 快照及首个 miss 合并，搜索筛选项也增加进程内 epoch 快照及首个 miss 合并。
- 文章详情复用已读取的公开文章；前后文章使用 tuple 排序条件；相关文章优先使用 taxonomy 投影；正文 HTML 使用 renderer/revision 版本键的可重建缓存，预览不进入共享公开缓存。
- `render_epoch` 在数据库连接 commit hook 后更新为进程内原子值；PageCache 命中走读锁，磁盘持久化使用固定 2 槽的异步 best-effort 写入。
- 搜索请求移除全量 `SyncAllDirty`；索引同步拆成 Reader 快照、事务外文本/gram 处理和短 Writer 批次；中文候选使用 `INTERSECT`，搜索结果缓存有固定条目数/字节上限和 bounded singleflight。
- 搜索页增加仅进程内的 HTML 快照：最多 64 条、最多 4 MiB，按 render epoch、主题版本和搜索索引版本隔离，成功结果由 RenderFlight 合并；不落盘，对外仍发送 `Cache-Control: no-store`。
- 站点名称、无封面媒体、归档和 taxonomy 等公开热点增加缓存或空输入短路；RenderFlight 测试改为等待并发 waiter 确实加入，避免固定 sleep 的 race 偶发失败。
- race 构建下的 10,000 篇规模查询采用单独的诊断阈值 5 s；非 race 构建仍使用 2 s 生产门槛，避免把 race 插桩成本误判为生产 SQL 回归。

主题插件、主题专用测试、默认主题视觉回归和已有 `.playwright-results/` 运行产物不在本轮性能修改范围内。

## 3. 实测环境与数据

- Go：`go1.26.5 linux/arm64`。
- 构建标签：`fts5 sqlite_omit_load_extension`。
- 数据库：SQLite WAL，Writer=1，默认 Reader=2；迁移版本 18。
- 代表性 fixture：10,000 篇已发布文章、单篇正文 5,824 bytes、100 个分类、500 个标签；每篇 1 个分类和确定性 1–4 个标签分布。
- HTTP fixture 当前没有封面媒体文件，媒体分支通过空 ID 短路覆盖；因此本报告不宣称已测出“带真实封面媒体”的容量。
- HTTP：完整应用路由 + `httptest.NewServer` + 临时数据库；未经过 Caddy、TLS、真实网卡或容器 CPU/内存限制。
- 默认 Reader=2；Reader=4/8 只作为应用层对照，不据此修改默认部署值。

完整矩阵命令：

```bash
PHASE3_LOAD_LEVELS=1,8,16,32,64,128 \
PHASE3_LOAD_SCENARIOS=cache-articles,cache-article,cold-article,search \
PHASE3_LOAD_REQUESTS=256 PHASE3_LOAD_COLD_REQUESTS=128 \
go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app \
  -run TestPhase3HTTPConcurrency -count=1 -v
```

这次当前代码的完整矩阵初始化耗时为：taxonomy projection 18.406 s，10,000 条搜索索引 82.333 s。索引重建是 fixture 初始化成本，不在普通搜索请求中同步执行。

## 4. 最新 10,000 篇完整矩阵

下表是当前代码、同一次完整矩阵的 p95。每个单元对应的场景均为 200 响应、`errors=0`、`status_failures=0`；搜索在 c1 顺序档位首次生成 HTML 快照，c8 及以上命中该快照。

| 并发 | `/articles` 缓存命中 | 单篇文章缓存命中 | 不同文章冷渲染 | `/search` 已完成索引 |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 23.115 ms | 3.027 ms | 45.258 ms | 3.510 ms |
| 8 | 13.982 ms | 10.699 ms | 259.681 ms | 11.259 ms |
| 16 | 26.199 ms | 17.766 ms | 927.880 ms | 104.017 ms |
| 32 | 101.771 ms | 79.968 ms | 1,584.162 ms | 65.821 ms |
| 64 | 69.462 ms | 24.287 ms | 1,972.696 ms | 133.124 ms |
| 128 | 150.271 ms | 110.783 ms | 3,471.852 ms | 180.806 ms |

冷文章场景 8/16/32/64/128 并发分别有 128/128 个 200 响应；Reader 等待为 1,701/15.213 s、1,726/50.241 s、1,721/108.565 s、1,710/145.979 s、1,712/276.271 s，Writer 等待始终为 0。这是当前剩余最明确的 SQLite Reader/冷渲染瓶颈。

按计划补跑的三轮完整档位 `1,2,4,8,16,32,64,128` 共 96 个场景单元，全部返回 200、错误 0、Writer 等待 0。重复轮次仍有明显宿主机波动：冷文章 c32 的 p95 中位数/范围为 1.823 s / 1.620–2.001 s，c64 为 2.826 s / 1.796–9.318 s，c128 为 3.333 s / 3.282–3.351 s；搜索 c128 为 115.418 ms / 111.951–369.820 ms；单篇缓存 c128 为 516.056 ms / 58.003–710.759 ms。因此这些三轮结果支持“无错误但尾延迟不稳定”的结论，不能作为稳定达标证据。

完整矩阵探针：分类 454.774 ms、标签 99.625 µs、归档索引 472.436 ms、已完成索引中文搜索服务 1.240 s，均无错误；这些是单次冷探针，不冒充 p95。首页探针返回 200。查询计划实际使用：

- taxonomy 分类/标签：`public_taxonomy_members_term_idx` covering index；
- 中文搜索候选：`search_grams_gram_idx` covering index + `INTERSECT` 临时候选集。

分类冷探针仍受宿主机调度影响，一次测量为 454.774 ms，不能据此宣称“连续三轮均 ≤150 ms”；标签在共享 taxonomy 快照命中后为 99.625 µs，epoch 快照会降低后续搜索请求的重复成本。

## 5. 100 RPS steady-state 实测

为区分首个 miss 与稳定命中，先通过 `PHASE3_LOAD_WARM_SEARCH=1` 访问同一个搜索 URL，再以目标 100 RPS 发出 256 个请求：

```bash
PHASE3_LOAD_WARM_SEARCH=1 \
PHASE3_LOAD_LEVELS=8 \
PHASE3_LOAD_SCENARIOS=cache-article,search \
PHASE3_LOAD_REQUESTS=256 PHASE3_LOAD_COLD_REQUESTS=32 \
PHASE3_LOAD_TARGET_RPS=100 \
go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app \
  -run TestPhase3HTTPConcurrency -count=1 -v
```

预热搜索请求本身耗时 705.286 ms，未计入下面的 256 请求负载：

| 场景 | 完成 RPS | p50 | p95 | p99 | 状态 | 缓存计数 |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| 单篇文章缓存 | 98.4 | 1.510 ms | 18.138 ms | 87.241 ms | 200:256，错误 0 | HIT 256 |
| 搜索 HTML 快照 | 99.1 | 5.122 ms | 180.508 ms | 241.732 ms | 200:256，错误 0 | HIT 256 |

两组负载的 Reader/Writer 等待均为 0。完成 RPS 略低于目标值是测试调度和整体计时造成的，不能写成精确 100.0 RPS。

未预热同一搜索 URL 的对照实测：c8、目标 100 RPS、256 请求中 `HIT=248`、`MISS=1`、`COALESCED=7`，首个查询/渲染等待使整体 p95 为 69.210 ms、p99 为 475.082 ms，完成 RPS 97.7；256/256 返回 200，错误 0。该结果保留为冷启动对照，不用它覆盖 steady-state 的尾延迟波动。

## 6. Reader 对照与冷路径

按计划使用相同 10,000 篇正文 fixture，针对冷文章路径实测 Reader=2/4/6/8；本组为 c8/32/64/128，跳过搜索索引和非必要探针，不改变文章读取路径：

| Reader | c8 p95 | c32 p95 | c64 p95 | c128 p95 | c32 Reader 等待 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 2 | 301.636 ms | 1,248.953 ms | 2,174.291 ms | 4,158.803 ms | 1,724 次 / 91.698 s |
| 4 | 348.085 ms | 1,385.607 ms | 2,265.916 ms | 3,823.356 ms | 1,690 次 / 93.653 s |
| 6 | 453.276 ms | 1,683.757 ms | 2,939.605 ms | 3,900.560 ms | 1,688 次 / 98.874 s |
| 8 | 391.684 ms | 1,801.841 ms | 2,372.009 ms | 4,050.336 ms | 1,651 次 / 98.067 s |

Reader=4 在 c128 单轮略低于 Reader=2，但整体没有稳定改善，且未在目标容器资源内测量 CPU/RSS/heap/WAL；因此不据此扩大默认连接池，仍保持 Reader=2。

## 7. 验收矩阵

| 验收项 | 实际证据 | 判定 |
| --- | --- | --- |
| 首页、分类、标签、搜索无 5xx | 最新完整矩阵全部 200；首页探针 200 | 通过 |
| taxonomy 查询使用有效投影/索引 | 投影状态 ready；分类/标签计划使用 covering index；读取无错误 | 通过（冷延迟仍需多轮确认） |
| 缓存约 100 RPS、p95 ≤25 ms、错误率 <0.1% | 预热 steady-state 文章缓存：98.4 RPS / 18.138 ms / 0 错误；搜索 HTML：99.1 RPS / 180.508 ms / 0 错误 | 文章缓存通过；搜索 p95 未达目标，需固定资源环境复测 |
| 首次公开渲染 p95 ≤150 ms | 最新完整矩阵 c1 冷文章 p95 45.258 ms | 通过单次应用层基线；未形成三轮稳定证据 |
| 冷文章 32 并发无 5xx | 128/128 返回 200，p95 1.584 s | 通过错误门禁；延迟未达 150 ms 目标 |
| 已完成索引搜索 p95 ≤250 ms | 最新完整矩阵最高 180.806 ms（c128）；steady-state 180.508 ms；冷目标 100 RPS p95 69.210 ms | 通过本轮应用层单轮；仍需固定资源环境多轮复测 |
| 搜索请求不触发全量 Writer 同步 | 搜索实现移除 `SyncAllDirty`；完整矩阵/steady-state Writer 等待均为 0 | 通过 |
| `make test` | 通过 | 通过 |
| `make test-race` | 完整通过，无 race 报告；race 规模查询诊断阈值单独放宽 | 通过 |
| `make vet` | 通过 | 通过 |
| `make perf-gate` | ADR-0031 和 phase-three scale gates 通过 | 通过 |
| `make stage3-acceptance` | Go tests/race/vet/module/perf 全部通过 | 通过（Docker live 分支被权限跳过） |
| Compose 资源限制、livez/readyz/Caddy 链路 | Docker API 返回 socket permission denied，未执行 | 未完成，不伪造 |

## 8. 工程门禁记录

已实际通过：

```text
make test
make test-race
make vet
make perf-gate
make stage3-acceptance
go mod verify
git diff --check
```

`make stage3-acceptance` 的最终输出为 `Stage three acceptance checks passed`，但脚本先检测到：

```text
permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock
```

所以该命令的应用层检查通过，依赖运行中 Compose 服务的 livez、readyz、首页和容器资源检查没有运行。`make build` 已通过；严格浏览器回归本次未执行，若后续 Docker/Playwright 权限可用，应继续执行它。

## 9. 剩余风险与下一步

1. 冷文章 32+ 并发的 Reader 等待仍是主要实际瓶颈；应在可控 CPU/内存容器中重新评估预热、冷渲染并发门和 Reader=2/4/8。
2. 搜索首个 miss 会把同一 key 的请求合并到一个较慢查询/渲染上；搜索本轮满足 250 ms 应用层搜索预算，但未稳定满足缓存路径 25 ms 目标，冷启动与宿主机调度尾延迟仍需单独优化或以预热任务降低。
3. taxonomy 冷探针在当前宿主机存在明显抖动，分类一次为 454.774 ms；需至少三轮固定环境结果后再宣称冷 p95 门槛稳定通过。
4. fixture 没有真实封面媒体；带图片解码、变体和本地存储读取的容量尚未覆盖。
5. Docker daemon 权限恢复后，必须补做 0.85 CPU/256 MiB、Go heap soft limit、RSS、GC、WAL、Caddy TLS 和健康检查，并据此决定部署 Reader 值。
