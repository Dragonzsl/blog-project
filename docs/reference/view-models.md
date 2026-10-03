# 主题视图参考

Theme API 为 `1`。实际字段定义与渲染映射位于 [`internal/presentation/theme.go`](../../internal/presentation/theme.go)，以下名称对应模板中的真实字段。

## 通用字段

| 字段 | 内容 |
|---|---|
| `.SiteName` | 站点名称 |
| `.Meta` | 标题、描述、canonical、RSS、社交图、时间与安全 JSON-LD |
| `.AssetURL`、`.ScriptURL`、`.SearchScriptURL` | 同源指纹资源 URL；没有对应脚本时可为空 |
| `.Navigation` | 主导航、页脚导航、当前路径、功能与侧栏视图 |
| `.Context` | `PageContext`，当前路径与功能标志 |
| `.Features` | 评论、评论模式、Newsletter 与统计标志 |
| `.Settings` | 当前主题 schema 校验后的设置副本 |

没有 `SiteView`、`NavigationView` 或 `MediaView` 这几个 Go 类型；它们是早期设计名称，不能直接据此编写模板。

## 页面字段

| 模板 | 数据 |
|---|---|
| `home.html` | `.Home`（`HomePageData`）：精选、最近文章、分类、归档、关于入口；`.Articles` 为兼容卡片列表 |
| `article.html` | `.Article`：标题、slug、摘要、`BodyHTML`、时间、封面、分类标签、阅读时间、目录、前后篇和相关内容；另有 `.Preview`、`.BackURL` |
| `listing.html` | `.Collection`（`CollectionView`）、`.Articles`、`.Pagination`、`.Title`、`.Description` |
| `directory.html` | `.Directory`（`DirectoryView`）：分类、标签或年月归档 |
| `search.html` | `.Search`（`SearchPageData`）：条件、选项、结果、高亮和分页 |
| `status.html` | `.Status`（`StatusView`）：状态码、标题和说明 |

文章与页面共用 `article.html`。服务侧 `ContentView` 带 Markdown，渲染器生成仅含清洗后 `BodyHTML` 的模板视图；模板不是数据库实体的直接序列化。

卡片类型为 `ArticleCard`，媒体类型为 `MediaData`（URL、替代文字、宽高、srcset）。`.Settings` 中 `media` 值由核心解析为 `MediaData`，`secret` 字段不会进入模板。

普通字符串由 `html/template` 自动转义。正文和搜索高亮的安全 HTML 由核心构造；主题不能增加通用 `safeHTML` 绕过。当前没有额外的日期、翻译或文件访问函数库，使用 Go template 内建函数与宿主预先整理的字段。
