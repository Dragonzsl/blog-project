# 改进阶段四实施记录：产品体验与公共能力

> 状态：核心实现完成，Docker 内部综合验收通过；宿主机公开入口受 Docker Desktop 路径共享限制。
> 日期：2026-09-11
> 设计基线：[阶段四实施计划](../development/phase-four-product-and-public-capabilities-plan.md)

## 1. 实施范围

阶段四已按 Q1–Q7 完成以下代码切片：

- Owner 与站点设置：后台公开站点元数据、密码/TOTP/恢复码维护、会话撤销、重新认证挑战和脱敏审计。
- Publishing 管理：版本正文按需读取、Markdown/元数据 diff、恢复前预览、恢复生成新版本、固定 50 项上限的批量操作和排程日历。
- 评论与 Newsletter：筛选分页、评论批量审核、操作幂等键、Newsletter 脱敏订阅列表、确认重发冷却和 Provider 状态。
- Content API：opaque cursor、分类/标签/更新时间过滤、ETag/Last-Modified/304、Bearer 私有缓存语义和查询边界。
- Discovery/Presentation：站点级 SEO 设置、canonical、Open Graph/Twitter、JSON-LD、语言/描述 Feed 元数据和条件缓存。
- Archive：v2 manifest、应用/迁移版本、站点公开设置、修订正文 checksum、媒体引用、重定向、dry-run、单内容原子导入与安全路径/文件集校验。

本阶段不包含主题插件视觉重做或提交。`themes/cel-panel/`、
`internal/presentation/cel_panel_theme_test.go` 和 `.playwright-results/` 保持独立，未作为阶段四范围纳入。

## 2. 数据库与兼容性

- 新增 [`00019_phase4_product_capabilities.sql`](../../db/migrations/00019_phase4_product_capabilities.sql)。
- 迁移增加站点公开元数据、安全挑战、批量操作持久化状态及 Newsletter 生命周期字段/索引。
- 启动日志已验证从迁移版本 18 升级到 19；空库和现有测试数据库均通过项目迁移测试。
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
git diff --check
```

性能门禁中的 10,000 篇内容规模结果：

```text
contents=10000 body_bytes=5824 published_page_size=50
published=19.488583ms admin=9.886ms cards=11.451166ms
heap_delta=-83136 bytes
```

归档测试覆盖 v2 修订正文、站点公开设置、重定向、媒体引用和 checksum 往返；Content API 测试覆盖 cursor、过滤、更新时间、304、Bearer 私有缓存和错误边界。`docker compose up --build -d` 已使用阶段四代码完成应用镜像构建，应用容器迁移至版本 19 并通过 `/livez`、`/readyz` 容器内检查。

补充的失败路径测试覆盖归档多版本导入的事务回滚、排程日历 `has_more` 边界和默认社交图片设置持久化。全量门禁在本次收口改动后再次通过。

## 5. 容器内真实链路验收

由于宿主机无法访问 Docker Desktop VM 内的 Compose 端口，本次使用同一 `personal-blog_blog_internal` 网络中的 Caddy、Playwright 和 curl 容器执行了真实 HTTP/TLS 链路检查，未绕过应用或 Caddy：

- HTTP Caddy：`/`、`/readyz` 均返回 200；首页包含 CSP、`X-Content-Type-Options`、ETag 和页面缓存头。
- HTTPS Caddy：`/readyz` 返回 HTTP/2 200，包含 `Via: 1.1 Caddy`、`Cache-Control: no-store` 和 `X-Content-Type-Options: nosniff`。
- 三浏览器 Playwright：27 项中 24 项通过、3 项跳过。跳过项是现有 `ARTICLE_SMOKE` 条件控制的文章 smoke 测试，当前隔离数据未提供 `/posts/markdown-migrated-post` fixture；默认主题的其余公共页面、响应头、搜索控件、文章表单和响应式抽屉检查全部通过。
- 无 JavaScript Chromium：首页、搜索、`/posts/demo-article-08` 和 `/admin/login` 均返回 200；分别验证了主内容、搜索表单、文章标题和后台登录 POST/CSRF 表单存在。

## 6. 未闭环项与原因

`make stage3-acceptance` 的 Go 测试、race、vet、模块校验和性能门禁部分通过；公开链路检查在访问 `https://localhost` 时失败。重建 Compose 时，Caddy 由于当前 Docker Desktop 未共享仓库路径 `/home/codex/workspace/blog-project/Caddyfile` 无法重新挂载，因此 Caddy 未启动，宿主机 80/443 与应用内网不可达。

因此以下两项仍没有宣称为宿主机入口通过：

- `make stage3-acceptance` 的宿主机 livez/readyz/home/admin HTTP 检查；
- `BROWSER_STRICT=1 make browser` 的默认 `https://localhost` 宿主机入口回归；同一套浏览器测试已经在 Docker 内部 Caddy 地址完成。

恢复 Docker Desktop 的文件共享后，应执行：

```bash
docker compose up --build -d
make stage3-acceptance
BROWSER_STRICT=1 make browser
```

除上述环境依赖和 ARTICLE_SMOKE fixture 缺失外，当前没有已知的阶段四代码失败项；阶段四不扩展性能预算，也不把主题插件视觉回归混入本阶段。

## 7. 收口与提交边界

阶段四设计文档已单独提交为 `fa2c419 docs(phase4): add product and public capabilities plan`。本次代码和实施记录尚未提交，后续提交应按设计文档第 10 节的模块边界拆分，并排除主题插件和运行产物。提交前需再次确认 `git diff --check`、敏感信息扫描和工作区中用户既有改动的归属。
