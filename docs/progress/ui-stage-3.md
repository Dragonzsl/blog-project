# 后台工作台阶段三实施记录

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

更新时间：2026-08-29

## 范围

本阶段对应《博客界面重构计划》的“后台工作台 P0”，目标是把站主从工作台到写作、发布、版本和回收站的核心路径组织成一套连续界面。阶段三不新增第三方字体、追踪脚本或重型动效，也不改变单站主、SQLite 和可选插件默认关闭的产品边界。

## 已完成

- 统一后台导航、移动端导航和页面级选中态；概览、内容库、媒体、内容组织、主题、插件与运维入口使用同一套后台外壳。
- 概览页接入真实编辑数据，展示草稿、定时发布、本周发布、待处理评论、最近编辑、最近发布和内容健康提示，并保留初始化成功状态。
- 文章和页面统一为内容库模式，支持关键词、状态、分类、标签和排序筛选，显示摘要、字数、阅读时间、更新时间、发布时间和公开链接。
- 将快速编辑、预览、版本、定时发布、撤回、取消定时、移入回收站等已有能力放入内容库和编辑器任务流。
- 编辑器改为桌面三栏工作区：左侧上下文与编辑指标、中间正文编辑、右侧发布设置和版本恢复；移动端自动变为单栏，并使用原生折叠面板承载次要设置。
- 后台动效只使用轻量的 opacity/transform，支持 `prefers-reduced-motion`；移动端控制区域保留足够触控尺寸，不新增外部资源。

## 主要实现位置

- 后台查询与视图模型：`internal/publishing/model.go`、`internal/publishing/repository.go`、`internal/publishing/service.go`
- 后台请求与路由：`internal/publishing/http.go`、`internal/identity/http.go`、`internal/comments/service.go`、`internal/app/app.go`
- 共享外壳与页面模板：`web/admin/templates/admin_shell.html`、`web/admin/templates/dashboard.html`、`web/admin/templates/contents.html`、`web/admin/templates/content_edit.html`
- 后台视觉、响应式与编辑器交互：`web/admin/static/admin.css`、`web/admin/static/admin.js`、`web/admin/static/editor.js`

## 验证

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  sh -ec "gofmt -w internal/publishing internal/identity internal/comments internal/app && go test -tags 'fts5 sqlite_omit_load_extension' ./..."

docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  sh -ec "go vet -tags 'fts5 sqlite_omit_load_extension' ./..."
```

当前全量 Go 测试和 `go vet` 均通过；`git diff --check` 无空白错误。容器重建后 app 健康，`/readyz` 返回 `200 application/json`。公开首页在 375px 和 1440px 视口下均未出现横向溢出；后台初始化入口因本地实例尚未创建站主而不提交初始化表单，受保护后台页面由模板和 HTTP 测试覆盖。

## 阶段三补充实施记录

根据实际查看和使用反馈，本补充已完成以下体验闭环：

- 公开端与后台增加亮色/暗色主题开关，跟随系统并持久化用户选择，补齐表面、表单、代码块和焦点状态的暗色对比度。
- Markdown 围栏代码显示语言并提供可访问的一键复制，复制内容不包含工具栏，且不引入第三方高亮依赖。
- 桌面端文章目录移动到正文侧栏，使用 sticky 定位和当前章节高亮；移动端改为独立的右侧全高目录抽屉，和全局站点导航互斥。
- 无目录文章自动切换为单列阅读列，修复正文被误放进目录列而产生的窄栏换行问题，并增加渲染回归用例。
- 搜索、筛选、分页等操作通过稳定锚点保持上下文，并将焦点定位到新结果，不强制回到页面顶部。
- 扩宽后台编辑器的中央编辑区，加入 Markdown 工具栏、快捷键和选区保持，同时保留服务端预览、自动保存、版本和发布工作流。
- 收敛全站展示字号和纵向间距，保持移动端正文至少 16px，以间距、标题比例和内容列宽优化信息密度。

详细的设计决策、实现顺序、性能边界和验收矩阵见《博客界面重构计划》的“阶段三补充：阅读效率与编辑效率”。本补充已通过 FTS5 标签下的全仓 Go 测试、主题 CSS 性能预算、公开端浏览器交互和移动端横向溢出检查。

## 下一阶段

阶段四已完成；详细实施记录见《阶段四实施记录》。后续以真实内容、窄屏设备和启停插件场景做持续回归。
