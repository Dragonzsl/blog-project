---
title: Safari 高度一致的表单控件
slug: safari-form-control-height
excerpt: 原生控件外观和字体渲染差异很细小，却足以让一个表单看起来不整齐。
category: 前端
tags: CSS, Safari, 无障碍
seo_title: Safari 表单控件一致性实践
seo_description: 通过显式高度、外观和焦点样式，让搜索表单在不同浏览器中保持一致。
date: 2026-08-26
---

# Safari 高度一致的表单控件

浏览器会为 `select` 和 `button` 施加自己的默认外观。设计稿看起来只有几像素的差异，实际并排时就会显得松散。

## 一个可靠的基线

- 使用 `box-sizing: border-box`。
- 同时设置 `height` 和 `min-height`。
- 在 Safari 上关闭原生外观，再补回清晰的自定义箭头。
- 使用 `:focus-visible` 保留键盘用户能看见的焦点环。

```css
.control {
  -webkit-appearance: none;
  appearance: none;
  height: 58px;
  line-height: 1.2;
}
```

最后不要只看 Chromium，至少在 Safari、Firefox 和一个窄视口上各测一次。

