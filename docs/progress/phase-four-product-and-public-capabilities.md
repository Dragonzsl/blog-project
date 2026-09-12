# 改进阶段四实施记录：产品体验与公共能力

> 状态：阶段四核心实现及本轮缺口收口完成；Docker 内部综合验收通过，宿主机公开入口仍受 Docker Desktop 路径共享限制。
> 日期：2026-09-12
> 设计基线：[阶段四实施计划](../development/phase-four-product-and-public-capabilities-plan.md)

## 1. 实施范围

阶段四已按 Q1–Q7 完成以下代码切片：

- Owner 与站点设置：后台公开站点元数据、Feed 摘要策略、公开能力状态、密码/TOTP/恢复码维护、会话撤销、重新认证挑战和脱敏审计。
- Publishing 管理：版本正文按需读取、Markdown/元数据 diff、恢复前预览（含编辑快照影响）、固定 50 项上限的批量操作、锁版本校验、回收站批量恢复和月/周排程日历；批量、移入回收站、历史版本恢复和回收站恢复均要求服务端确认，禁用 JavaScript 仍不能绕过。
- 评论与 Newsletter：状态/文章/日期/关键词筛选分页、评论批量审核计数与操作幂等键、公开评论上限和审批边界、Newsletter 脱敏订阅列表、确认重发冷却和 Provider 状态；单项/批量审核与确认邮件重发均有服务端确认。
- Content API：签名 opaque cursor、筛选条件绑定、24 小时过期、分类/标签/更新时间过滤、列表轻量投影、批量媒体读取、ETag/Last-Modified/304、Bearer 私有缓存语义、按客户端限流和查询边界。
- Discovery/Presentation：站点级 SEO 设置、canonical、Open Graph/Twitter、JSON-LD、语言/描述 Feed 元数据和条件缓存。
- Archive：v2 manifest、应用/迁移版本、站点公开设置、Feed 策略、稳定内容/版本公共 ID、修订正文 checksum、正文媒体引用校验、重定向幂等、dry-run、导入失败回滚与安全路径/文件集校验。

本阶段不包含主题插件视觉重做或提交。`themes/cel-panel/`、
`internal/presentation/cel_panel_theme_test.go` 和 `.playwright-results/` 保持独立，未作为阶段四范围纳入。

## 2. 数据库与兼容性

- 新增 [`00019_phase4_product_capabilities.sql`](../../db/migrations/00019_phase4_product_capabilities.sql) 和 [`00020_feed_summary_mode.sql`](../../db/migrations/00020_feed_summary_mode.sql)。
- 迁移增加站点公开元数据、Feed 策略、安全挑战、批量操作持久化状态及 Newsletter 生命周期字段/索引。
- 启动日志已验证从迁移版本 19 升级到 20；空库和现有测试数据库均通过项目迁移测试。
- 归档保持 v1 校验/导入兼容，新的导出格式为 v2；v2 导入支持 dry-run，站点设置应用通过显式回调完成。

## 3. 关键实现位置

| 能力 | 主要实现 |
| --- | --- |
| Owner/站点设置 | `internal/identity/{model,repository,service,http}.go`、`web/admin/templates/{settings,security_recovery}.html` |
| diff/恢复/批量/日历 | `internal/publishing/{diff,bulk,http,service,repository}.go`、`web/admin/templates/{revision_compare,restore_preview,bulk_result,schedule_calendar}.html` |
| 评论/Newsletter | `internal/comments/{http,service,plugin}.go`、`internal/notifications/{newsletter,plugin}.go` |
| API 增量协议 | `internal/contentapi/plugin.go`、`internal/contentapi/plugin_test.go`、`docs/api/content-api-v1.md` |
| SEO/Feed | `internal/presentation/{http,theme}.go`、`internal/discovery/service.go`、`internal/app/app.go` |
| 归档 v2 | `internal/archive/archive.go`、`internal/archive/archive_test.go`、`cmd/blog/main.go` |

## 4. 已完成验证

以下命令在固定构建标签 `fts5 sqlite_omit_load_extension` 下通过：

```text
make test
make test-race
make vet
go mod verify
make perf-gate
docker compose config
git diff --check
```

性能门禁中的 10,000 篇内容规模结果：

```text
contents=10000 body_bytes=5824 published_page_size=50
published=19.488583ms admin=9.886ms cards=11.451166ms
heap_delta=-83136 bytes
```

Content API 的 10,000 篇文章真实应用路由压测（`internal/app/phase3_http_load_test.go`，构建标签 `phase3load`）结果：

```text
contents=10000 pages=100 duration=22.478661678s rps=444.9
page_p50=210.900583ms page_p95=302.887ms
updated_since_items=100 updated_since_duration=517.002751ms
```

复现命令：

```bash
go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app -run TestPhase4ContentAPICursorLoad -count=1 -v
```

测试同时校验了 100 页连续游标没有重复/遗漏，并确认 `updated_since` 返回的 100 条记录均属于完整游标集合。归档测试覆盖 v2 稳定公共 ID、修订正文、站点公开设置、重定向、媒体引用和 checksum 往返；Content API 单元/HTTP 测试覆盖签名/篡改/跨筛选 cursor、过滤、更新时间、304、Bearer 私有缓存和错误边界。应用镜像已按当前工作树重建，容器日志确认迁移版本为 20，`/readyz` 返回 200，CLI `status` 也报告 `migration_version: 20`。

补充的失败路径测试覆盖归档多版本导入的事务回滚、重复公共 ID dry-run 冲突、回收站恢复锁版本、排程日历 `has_more` 边界、默认社交图片设置持久化、游标稳定 ETag，以及发布/恢复/评论审核/Newsletter 重发缺少显式确认时不写入的 HTTP 路径。期间修复评论批量汇总写入严格 SQLite `TEXT` 字段时的 `[]byte` 类型错误。

## 5. 容器内真实链路验收

历史验收曾由于宿主机无法访问 Docker Desktop VM 内的 Compose 端口，使用同一 `personal-blog_blog_internal` 网络中的 Caddy、Playwright 和 curl 容器执行真实 HTTP/TLS 链路检查，未绕过应用或 Caddy；本轮宿主机复查仍未得到可用公开入口：

- HTTP Caddy：`/`、`/readyz` 均返回 200；首页包含 CSP、`X-Content-Type-Options`、ETag 和页面缓存头。
- HTTPS Caddy：`/readyz` 返回 HTTP/2 200，包含 `Via: 1.1 Caddy`、`Cache-Control: no-store` 和 `X-Content-Type-Options: nosniff`。
- 三浏览器 Playwright：27 项中 24 项通过、3 项跳过。跳过项是现有 `ARTICLE_SMOKE` 条件控制的文章 smoke 测试，当前隔离数据未提供 `/posts/markdown-migrated-post` fixture；默认主题的其余公共页面、响应头、搜索控件、文章表单和响应式抽屉检查全部通过。
- 无 JavaScript Chromium：首页、搜索、`/posts/demo-article-08` 和 `/admin/login` 均返回 200；分别验证了主内容、搜索表单、文章标题和后台登录 POST/CSRF 表单存在。

## 6. 未闭环项与原因

本轮 `docker compose ps` 只能看到应用容器，宿主机 80/443 和应用映射端口均不可达；`make browser` 另外报告 Chromium、Firefox、WebKit 的 Playwright 可执行文件尚未安装。因此 Caddy/TLS、浏览器和 Compose 资源约束结果仍不能作为本轮代码验收通过。

因此以下两项仍没有宣称为宿主机入口通过：

- `make stage3-acceptance` 的宿主机 livez/readyz/home/admin HTTP 检查；
- `BROWSER_STRICT=1 make browser` 的默认 `https://localhost` 宿主机入口回归；本轮 `make browser` 因浏览器二进制和宿主机入口均不可用而失败。

恢复 Docker Desktop 的文件共享后，应执行：

```bash
docker compose up --build -d
make stage3-acceptance
BROWSER_STRICT=1 make browser
```

除上述环境依赖、ARTICLE_SMOKE fixture 缺失和真实封面媒体容量 fixture 外，当前没有已知的阶段四代码失败项；阶段四不扩展性能预算，也不把主题插件视觉回归混入本阶段。

## 7. 本轮缺口收口

本轮对照阶段四设计逐项复核后，已补齐设置中的 Feed/公开能力状态、恢复预览中的编辑快照影响、内容批量锁版本和回收站恢复、月/周排程、危险管理操作的无 JavaScript 服务端确认、评论管理边界、Content API 游标完整性与媒体批量读取，以及归档稳定 ID/正文媒体校验/失败清理。列表 API 继续采用阶段三的轻量卡片投影，正文仅由单项接口返回，并已同步更新 API 契约文档。

Content API 的 1 万篇文章 cursor/`updated_since` 证据已补齐。仍未宣称为生产验收通过的只有环境证据项：宿主机 Caddy 入口、严格浏览器回归、0.85 CPU/256 MiB 资源约束压测和真实封面媒体容量 fixture；这些不是本轮代码缺口。

## 8. 收口与提交边界

阶段四设计文档已单独提交为 `fa2c419 docs(phase4): add product and public capabilities plan`。本轮代码和实施记录尚未提交，后续提交应按设计文档第 10 节的模块边界拆分，并排除主题插件和运行产物。提交前需再次确认 `git diff --check`、敏感信息扫描和工作区中用户既有改动的归属。
