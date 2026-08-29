# 公开阅读端阶段二实施记录

更新时间：2026-08-28

## 已完成

- 首页拆分为精选、最近文章、主题分类、时间归档和关于入口；没有内容时提供可执行空状态。
- 新增 `/categories`、`/tags`、`/archive` 和 `/archive/{year}/{month}`，索引只展示有公开文章的分类和标签，归档按 UTC 年/月聚合并分页。
- 文章详情增加同源指纹化 `theme.js`，支持阅读进度、目录当前位置、复制链接、系统分享回退和打印；关闭 JavaScript 时核心阅读链路仍可用。
- 搜索筛选由分类/标签 Slug 输入升级为公开选项，结果支持安全关键词高亮、保留筛选态和无结果行动入口。
- 增加统一 404、405、500 状态页；异常页面使用 `no-store` 和 `noindex`，重定向仍优先处理。
- Sitemap 纳入文章、归档、分类和标签索引入口；主题包兼容旧主题，新增目录/状态模板可由默认主题回退。

## 性能与安全约束

- 阅读脚本无第三方依赖，仅在文章页按需加载；滚动监听使用 passive listener 和 `requestAnimationFrame`。
- CSS 动效仅使用 opacity/transform，保留 `prefers-reduced-motion` 和打印样式。
- 高亮文本先 HTML 转义，再插入 `<mark>`；脚本 CSP 只允许同源资源和已有 JSON-LD 哈希。
- 首页与索引继续使用页面缓存；搜索和错误状态保持 `no-store`。

## 验证

```bash
go test -tags 'fts5 sqlite_omit_load_extension' ./internal/presentation ./internal/discovery ./internal/organization ./internal/publishing
```

阶段二端到端测试位于 `internal/presentation/phase_two_test.go`，覆盖首页、分类/标签、归档索引与月份页、搜索高亮与筛选保持、文章增强交互、脚本资源、404 和 405。
