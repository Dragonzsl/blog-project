# 主题开发手册

主题是受限的 Go `html/template` 资源包，不是服务端代码。分发包至少包含 `theme.json`、`templates/*.html`、`assets/theme.css` 和许可证；建议同时提供 `preview.webp` 与一组固定夹具。

## 清单

```json
{
  "id": "org.example.paper",
  "name": "Paper",
  "version": "1.0.0",
  "themeApi": "1",
  "core": ">=1.0.0 <2.0.0",
  "features": ["dark-mode", "toc"]
}
```

安装器会拒绝绝对路径、`..`、符号链接、特殊文件、超出文件/解压/包大小上限的 ZIP，并解析所有模板后才原子发布。启用前必须通过空站、长文、中英文混排、大图、无封面、长标签、404 和预览夹具。

## 可用视图

模板只接收版本化的公开 `SiteView`、`ContentView`、`CollectionView`、`NavigationView`、`MediaView` 和 `PageContext`。正文已经由核心清洗为安全 HTML；不要在主题内引入 `safeHTML` 或读取环境变量、文件、数据库。

## 插槽与性能

标准插槽为 `head.metadata`、`body.start`、`article.before`、`article.after`、`article.comments`、`body.end`。主题自行决定位置，但声明支持某个能力后必须渲染相应插槽。默认主题 CSS 压缩后不超过 40 KiB，阅读必需 JavaScript 不超过 15 KiB；不要默认请求外部字体、分析脚本或图标 CDN。

## 本地验证

```bash
blog theme install --archive paper.zip
blog theme list
blog theme activate --id org.example.paper --version 1.0.0
blog theme rollback
```

构建前运行 `go test -tags 'fts5 sqlite_omit_load_extension' ./...`、`make perf-gate` 和浏览器回归夹具。主题损坏时内嵌默认主题始终可回退。
