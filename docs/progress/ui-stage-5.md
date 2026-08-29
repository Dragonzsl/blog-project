# 全站侧边栏导航阶段五实施记录

更新时间：2026-08-29

## 目标

把公开阅读端、文章目录和站主管理端从各自独立的顶部/折叠菜单统一为可预测的侧栏导航体系，保留服务端渲染和原生 HTML 降级，不引入重型前端依赖。

## 已完成

- 公共端在 1100px 以上使用 248px 常驻左栏；1100px 及以下使用左侧全高抽屉，覆盖首页、文章、分类、标签、归档、搜索、状态页和主题预览。
- 后台在 1024px 以上使用工作台常驻左栏；1023px 及以下使用左侧全高抽屉，覆盖内容库、编辑器、媒体、组织、主题、插件和运维入口。
- 服务端新增安全的 `SidebarView`、分组、活动态和递归子项视图模型；保留原有 `Primary`/`Footer` 数据，避免自定义主题导航失效。
- 文章目录与全局导航使用独立抽屉状态：桌面目录位于正文左侧并 sticky，移动目录从右侧展开；两者互斥，点击目录锚点后关闭并保持阅读位置。
- 抽屉统一支持汉堡按钮、关闭按钮、遮罩、Escape、Tab 焦点循环、焦点回收、当前入口高亮和 `prefers-reduced-motion`；所有图标使用同源 SVG。
- 无目录文章使用单列阅读布局且不输出目录触发器；无 JavaScript 时公共端窄屏导航退化为页面内静态链接。

## 断点契约

| 区域 | 常驻侧栏 | 抽屉侧栏 | 抽屉方向 |
| --- | --- | --- | --- |
| 公共全局导航 | `>= 1101px` | `<= 1100px` | 左侧 |
| 公共文章目录 | `>= 761px` | `<= 760px` | 右侧 |
| 后台工作台导航 | `>= 1024px` | `<= 1023px` | 左侧 |

## 验证

- `go test -tags 'fts5 sqlite_omit_load_extension' ./...` 通过。
- `go test -race -tags 'fts5 sqlite_omit_load_extension' ./...`、`go vet`、`go mod verify` 通过。
- `node --check themes/default/assets/theme.js`、`node --check web/admin/static/admin.js` 和 `git diff --check` 通过。
- 默认主题 CSS 为 40,836 bytes，阅读端 JS 为 12,107 bytes，保持既有 40 KiB / 15 KiB 预算。
- 内置 Chromium 真实回归覆盖 375px、1280px：首页与文章侧栏、目录抽屉、遮罩、Escape、主题切换、无目录文章、滚动位置和横向溢出均通过。
- Compose app 保持 healthy，`/livez`、`/readyz` 正常；未登录访问后台仍返回登录重定向。跨浏览器脚本因当前环境未安装 Playwright 浏览器而跳过。

## 主要实现位置

- 公共导航视图：`internal/presentation/theme.go`、`internal/presentation/http.go`
- 公共模板与交互：`themes/default/templates/theme_shell.html`、`themes/default/templates/article.html`、`themes/default/assets/theme.css`、`themes/default/assets/theme.js`
- 后台模板与交互：`web/admin/templates/admin_shell.html`、`web/admin/static/admin.css`、`web/admin/static/admin.js`
- 契约回归：`internal/presentation/phase_five_test.go`
