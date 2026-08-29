# 插件与视觉收尾阶段四实施记录

更新时间：2026-08-29

## 范围

本阶段对应《博客界面重构计划》的“插件和视觉收尾 P1”。目标是在不改变单站主、SQLite、服务端渲染和可选插件默认关闭边界的前提下，补齐评论、Newsletter、统计的界面插槽，整理媒体/主题/插件后台，并把空状态、错误状态、通知和表单提交反馈统一起来。

## 已完成

- 公开文章接入评论插槽：本地评论异步读取已审核评论，支持称呼、邮箱、网站、留言、蜜罐字段、审核提示和原位提交反馈；外部评论提供安全托管入口。Unicode slug 在评论查询和外部地址中保持一致。
- 首页、文章列表和文章详情按插件启用状态渲染 Newsletter 插槽；订阅请求显示提交中、成功和失败状态，不刷新页面、不改变滚动位置。
- 插件管理页显示启停状态、插件作用范围、UI 归属、能力声明和初始化结果；启停及评论审核使用成功/失败通知，并维持原有 CSRF 与插件隔离。
- 媒体库增加图片替代文字、格式筛选、网格/列表切换、Markdown 复制和引用数量；引用计数复用已有 `media_references`，未改变删除保护逻辑。
- 主题管理增加 `preview.webp/png/jpg/jpeg` 的固定文件名读取、预览图展示、预览入口和默认主题安全回退；预览图片不允许任意路径访问。
- 统计页增加近 7/30/90 天切换、摘要指标、隐私说明、空状态；公开页面不暴露统计数据或追踪脚本。
- 共享后台外壳提供 `admin_feedback`，覆盖插件、评论、媒体、主题、统计、编辑、组织、初始化、登录和重定向页面；后台提交控件提供处理中状态。新增样式沿用 token、最小触控尺寸和 reduced-motion 规则。
- 全站导航改为统一的侧栏模式：桌面端公共端使用 248px 常驻左栏，后台保留工作台常驻左栏；窄屏端使用左侧全高抽屉，支持汉堡菜单、关闭、遮罩、`Esc`、焦点循环和当前入口高亮。
- 文章目录与全局导航解耦：桌面端使用正文左侧 sticky 目录，窄屏端使用右侧全高目录抽屉；两个抽屉互斥，点击锚点后关闭并保持阅读位置。
- 根据 `localhost-20260829T163552.json` 的 Lighthouse 结果补齐同源 `connect-src`，避免 Chrome DevTools 探测请求触发 CSP 控制台/Issues 报警；新增动态 Markdown `llms.txt`，并为 `robots.txt` 增加状态与内容类型回归覆盖。报告中的性能与无障碍满分保持不变。

## 主要实现位置

- 公开主题插槽与交互：`themes/default/templates/theme_shell.html`、`themes/default/templates/home.html`、`themes/default/templates/listing.html`、`themes/default/templates/article.html`、`themes/default/assets/theme.js`、`themes/default/assets/theme.css`
- 插件/评论/统计：`internal/extensions/host.go`、`internal/extensions/http.go`、`internal/comments/http.go`、`internal/analytics/http.go`
- 媒体/主题后台：`internal/media/model.go`、`internal/media/repository.go`、`internal/media/http.go`、`internal/presentation/theme_http.go`
- 管理界面：`web/admin/templates/admin_shell.html`、`web/admin/templates/plugins.html`、`web/admin/templates/comments.html`、`web/admin/templates/media.html`、`web/admin/templates/themes.html`、`web/admin/templates/analytics.html`、`web/admin/static/admin.js`、`web/admin/static/admin.css`
- 回归用例：`internal/presentation/phase_four_test.go`

## 验证

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  go test -tags 'fts5 sqlite_omit_load_extension' ./...

docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  go vet -tags 'fts5 sqlite_omit_load_extension' ./...

node --check themes/default/assets/theme.js
node --check web/admin/static/admin.js
git diff --check
curl -i http://localhost/robots.txt
curl -i http://localhost/llms.txt
```

全量 Go 测试、`go vet`、JavaScript 语法检查和 `git diff --check` 均通过。默认主题资源为 CSS 40,836 bytes、JS 12,107 bytes，仍满足 40 KiB / 15 KiB 预算。Docker 镜像已重建，app 健康检查通过。

公开端浏览器回归确认：首页和文章页正常渲染、无横向溢出、无目录文章使用单列布局，主题开关可在暗/亮之间切换并保持当前位置；评论插槽在启用时出现、关闭时不渲染。后台页面在当前浏览器未登录时正确回到登录页，受保护管理流程由 Go HTTP 测试和模板解析覆盖。

## 约束

本阶段没有新增外部字体、第三方脚本、代码高亮库、追踪器或重型前端框架；评论和订阅是可选增强，文章阅读、导航、列表、搜索和主题基础切换仍保留服务端/原生 HTML 降级路径。
