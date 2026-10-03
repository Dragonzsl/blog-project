# 澄光编辑室主题

独立主题包，源码在 [`themes/luminous-editorial`](../../themes/luminous-editorial)，ID 为 `luminous-editorial`，当前版本 `1.0.0`，Theme API 为 `1`。

## 页面与视觉

系统字体、紫色强调色、浅色/深色模式。首页包含站点介绍、精选文章、最近文章与内容光谱；文章页提供阅读进度、目录、上一篇/下一篇和评论/Newsletter 插槽。样式和脚本来自同源资源。

模板使用现有公开视图模型，不新增数据库字段或服务端能力。没有 JavaScript 时仍可浏览内容、提交搜索、使用导航和文章目录。

| 视口 | 导航与目录 |
|---|---|
| 宽度 ≥1440px | 顶部导航，文章目录侧栏 |
| 1024–1439px | 顶部导航，文章目录抽屉 |
| ≤1023px | 站点导航与目录分别使用抽屉 |

抽屉支持 Esc 关闭与焦点恢复。无 JavaScript 时目录按文档流展示；减弱动态效果偏好由 CSS/JS 响应。

## 设置

设置以 [`theme.json`](../../themes/luminous-editorial/theme.json) 为准：

| 字段 | 默认值 | 用途 |
|---|---|---|
| `accent` | `#6C5CE7` | 强调色 |
| `showSpectrum` | `true` | 首页内容光谱 |
| `showReadingTime` | `true` | 阅读时间 |

颜色通过 `<style>` 元素输出，由核心生成精确 CSP 哈希，不使用内联 style 属性。

## 打包与验证

```bash
mkdir -p dist
(cd themes/luminous-editorial && zip -qr ../../dist/luminous-editorial-1.0.0.zip theme.json templates assets LICENSE)
```

从后台 `/admin/themes` 安装、预览和激活。主题设置、移除和回退规则见[主题开发](themes.md)。

Go 渲染测试位于 `internal/presentation/luminous_editorial_theme_test.go`，浏览器测试位于 `tests/browser/luminous.spec.ts`。使用启用了该主题的隔离站点和含二级标题的已发布文章：

```bash
LUMINOUS_BASE_URL=https://localhost \
LUMINOUS_ARTICLE_FIXTURE=/posts/example \
BROWSER_STRICT=1 make browser
```

定制颜色时可传入 `LUMINOUS_ACCENT`。测试检查桌面导航、目录断点、焦点恢复和无 JavaScript 样式。

早期页面草图与未采用的动效方案见 [Git 历史](https://github.com/Dragonzsl/blog-project/blob/60b3262/docs/development/luminous-editorial-theme-design.md)。
