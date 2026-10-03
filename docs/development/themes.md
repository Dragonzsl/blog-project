# 主题开发手册

主题是受限的 Go `html/template` 资源包，不是服务端代码。分发包至少包含 `theme.json`、`templates/*.html`、`assets/theme.css` 和许可证；可选提供 `assets/theme.js`，建议同时提供 `preview.webp` 与一组固定夹具。主题设置由 manifest 的 `settingsVersion` 标识版本。

内嵌默认主题采用宋韵纸墨与圆润卡片的视觉方向：暖纸色、松绿、少量朱砂、宋体与楷体回退，以及桌面横向导航。页眉采用紧凑的半透明圆角表面、柔和阴影和低饱和选中态；首页滚动时固定于视口顶部，其他页面页眉随页面滚动。窄屏导航在页眉第二行横向滚动，自定义导航及其子项保持可访问。公开页面使用本地原创水墨山水作为低对比度全页背景，不添加裁切山水插画或首页装饰图。背景图作为嵌入资源按内容指纹提供一年缓存，不依赖外部服务。首页、文章列表、归档和文章详情分别对应独立页面；搜索通过页眉图标打开 Spotlight 式对话框，`/search` 保留为无 JavaScript 时的服务端搜索提交与结果入口。主题样式按基础、宋韵和圆润覆盖层顺序组合，交互仍以服务端渲染为主，并支持浅色/深色/系统模式、键盘操作、窄屏布局、辅助功能及资源预算。

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

## 可用视图

模板只接收版本化的公开 `SiteView`、`ContentView`、`CollectionView`、`HomePageData`、`DirectoryView`、`SearchPageData`、`StatusView`、`NavigationView`、`MediaView` 和 `PageContext`。正文已经由核心清洗为安全 HTML；不要在主题内引入 `safeHTML` 或读取环境变量、文件、数据库。旧主题缺少新增目录/状态模板时，核心会提供内嵌的默认目录和状态回退模板；如果主题提供了这两个文件，则优先使用主题自己的实现。

所有页面模板都可通过 `.Settings` 读取宿主校验后的设置副本。只允许 manifest schema 中声明的键；`secret: true` 的设置不会进入模板，`media` 设置只保存稳定媒体公共 ID并在运行时解析为 `MediaData`，不暴露内部媒体行号、对象键或哈希。URL、颜色、整数、选项、字符串长度和总 JSON 大小由宿主校验。

## 插槽与性能

标准插槽为 `head.metadata`、`body.start`、`article.before`、`article.after`、`article.comments`、`body.end`。主题自行决定位置，但声明支持某个能力后必须渲染相应插槽。默认主题 CSS 压缩后不超过 40 KiB，阅读必需 JavaScript 不超过 15 KiB；脚本资源必须同源并使用指纹 URL。不要默认请求外部字体、分析脚本或图标 CDN。

## 本地验证

```bash
blog theme install --archive paper.zip
blog theme list
blog theme activate --id org.example.paper --version 1.0.0
blog theme rollback
```

构建前运行 `go test -tags 'fts5 sqlite_omit_load_extension' ./...`、`make perf-gate` 和浏览器回归夹具。主题损坏时内嵌默认主题始终可回退。数据库 `themes.active` 是激活权威，`active.json` 只是可修复缓存；管理端设置保存需要 CSRF/Origin 保护并使 `render_epoch` 递增。主题回退保留已安装包、设置和激活历史。

### 主题删除与重新添加

后台主题页允许删除未启用的已安装主题。删除以 `themes.removed_at` 标记，从已安装列表移除，保留主题包、设置和切换历史；不能删除当前主题或内嵌默认回退主题。删除需要登录、CSRF 校验与显式确认，重复操作不会重复写审计记录。已删除主题不能启用、预览或修改设置；重新添加会重新校验包、目录校验和与设置，且不会自动启用。若回退历史指向已删除主题，则使用内嵌默认主题。主题删除不会释放磁盘空间。
