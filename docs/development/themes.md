# 主题开发手册

主题是受限的 Go `html/template` 资源包，不是服务端代码。分发包至少包含 `theme.json`、`templates/*.html`、`assets/theme.css` 和许可证；可选提供 `assets/theme.js`，建议同时提供 `preview.webp` 与一组固定夹具。主题设置由 manifest 的 `settingsVersion` 标识版本。

内嵌默认主题使用本地水墨背景、顶部导航和浅色/深色模式，搜索对话框提供渐进增强，`/search` 保留完整服务端入口。资源由 `themes/default/embed.go` 嵌入。

## 包结构

```text
theme.json
templates/
  home.html
  article.html
  listing.html
  search.html
  navigation.html
  directory.html       可选，缺省使用内嵌回退
  status.html          可选，缺省使用内嵌回退
assets/
  theme.css
  theme.js             可选
LICENSE
```

其他局部模板可放入 `templates/` 并通过标准 Go template 语法调用。页面内容也使用 `article.html`，没有独立 `page.html` 契约。

## 清单

```json
{
  "id": "org.example.paper",
  "name": "Paper",
  "version": "1.0.0",
  "themeApi": 1,
  "core": ">=1.0.0 <2.0.0",
  "features": ["dark-mode", "toc"],
  "settingsVersion": 1,
  "settingsSchema": {
    "accent": {"type": "color", "default": "#3157d5"},
    "hero": {"type": "media"},
    "showReadingTime": {"type": "boolean", "default": true}
  }
}
```

安装器会拒绝绝对路径、`..`、符号链接、特殊文件、超出文件/解压/包大小上限的 ZIP，并解析所有模板后才原子发布。启用前必须通过空站、长文、中英文混排、大图、无封面、长标签、404 和预览夹具。安装时同时保存 ZIP checksum 与按目录文件路径/内容计算的确定性 checksum；激活和启动 reconcile 会再次复核。

仓库内的 `themes/cel-panel` 是一个可直接上传的「番剧信号站 / Anime Signal」主题包示例，采用番剧放送台、漫画网点、速度线、硬边框和原创 TV 角色海报视觉语言，支持浅色/暗色/跟随系统、响应式导航、文章目录、评论和 Newsletter 插槽。打包时应让 `theme.json` 位于 ZIP 根目录，例如：

```bash
mkdir -p dist
(cd themes/cel-panel && zip -qr ../../dist/cel-panel-2.2.4.zip theme.json templates assets LICENSE)
```

澄光编辑室（`luminous-editorial`）的完整设计与实现说明见[澄光编辑室主题设计](./luminous-editorial-theme-design.md)。它是独立主题包，不改变默认主题或已有主题包。

## 可用视图

模板接收公开字段映射，页面数据和字段名见[视图参考](../reference/view-models.md)。正文已经由核心清洗为安全 HTML；不要在主题内引入 `safeHTML` 或读取环境变量、文件、数据库。旧主题缺少新增目录/状态模板时，核心会提供内嵌的默认目录和状态回退模板；如果主题提供了这两个文件，则优先使用主题自己的实现。

所有页面模板都可通过 `.Settings` 读取宿主校验后的设置副本。只允许 manifest schema 中声明的键；`secret: true` 的设置不会进入模板，`media` 设置只保存稳定媒体公共 ID并在运行时解析为 `MediaData`，不暴露内部媒体行号、对象键或哈希。URL、颜色、整数、选项、字符串长度和总 JSON 大小由宿主校验。颜色等动态样式应输出到 `<style>` 元素：核心为最终渲染内容生成精确 CSP SHA-256 授权，支持无 JavaScript 展示。内联 `style` 属性仍被禁止，不要使用 `unsafe-inline`。

## 插槽与性能

当前没有通用的命名 HTML 插槽注册 API。评论和 Newsletter 按 `.Features` 在模板中呈现对应入口，可参考默认主题；插件的 `RouteSlot` 只用于注册宿主保留的公共路径。默认主题 CSS 压缩后不超过 40 KiB，阅读必需 JavaScript 不超过 15 KiB；脚本资源必须同源并使用指纹 URL。不要默认请求外部字体、分析脚本或图标 CDN。

## 本地验证

```bash
blog theme install --archive paper.zip
blog theme list
blog theme activate --id org.example.paper --version 1.0.0
blog theme rollback
```

澄光主题的三浏览器交互回归使用隔离站点：设置 `LUMINOUS_BASE_URL` 为已启用该主题的站点地址，`LUMINOUS_ARTICLE_FIXTURE=/posts/<slug>` 为包含二级标题的已发布文章，然后运行 `make browser`。若修改了主题色，可通过 `LUMINOUS_ACCENT` 指定预期颜色值；测试覆盖桌面导航、目录断点、Esc 焦点恢复和无 JavaScript 的 CSP 样式。

构建前运行 `go test -tags 'fts5 sqlite_omit_load_extension' ./...`、`make perf-gate` 和浏览器回归夹具。主题损坏时内嵌默认主题始终可回退。数据库 `themes.active` 是激活权威，`active.json` 只是可修复缓存；管理端设置保存需要 CSRF/Origin 保护并使 `render_epoch` 递增。主题回退保留已安装包、设置和激活历史。

### 主题删除与重新添加

后台主题页允许删除未启用的已安装主题。删除以 `themes.removed_at` 标记，从已安装列表移除，保留主题包、设置和切换历史；不能删除当前主题或内嵌默认回退主题。删除需要登录、CSRF 校验与显式确认，重复操作不会重复写审计记录。已删除主题不能启用、预览或修改设置；重新添加会重新校验包、目录校验和与设置，且不会自动启用。若回退历史指向已删除主题，则使用内嵌默认主题。主题删除不会释放磁盘空间。
