# 主流博客系统对照

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

本项目不复制某一个现有系统，而是有意识地借鉴其成熟部分，并拒绝与本项目边界冲突的部分。

| 系统 | 借鉴 | 不照搬 |
|---|---|---|
| Ghost | 聚焦写作的后台、草稿/版本/定时发布、可安装主题、导入导出、RSS/会员之外的出版体验 | Node/MySQL 运行栈、会员与付费订阅、邮件业务成为核心 |
| WordPress | 用户熟悉的文章/页面/分类/标签/媒体/重定向，WXR 迁移，主题与插件是不同概念 | 任意 PHP 插件执行、主题夹带业务、层级自定义内容类型、全局钩子污染 |
| Hugo | 极快公开输出、主题资源包、受限模板函数、内容和展示分离 | Git/文件作为内容权威来源、发布时整站构建、站主必须使用工具链 |
| WriteFreely | Go 单程序、SQLite、自托管、简洁写作界面和低运维形态 | 多博客/社区/ActivityPub 产品边界，过度简化的内容管理能力 |
| Payload | 配置驱动模块、版本/草稿、后台扩展点、插件清单思想 | Next.js/React CMS 运行时、通用内容建模和更高常驻资源 |

## Ghost

Ghost 的官方文档把主题、Content API、迁移、会员和 Newsletter 组织成清晰的出版产品能力。本项目借鉴其写作与主题体验，但 ADR-0014 已明确排除协作者、读者账户和付费会员。[Ghost 文档](https://ghost.org/docs/)、[主题安装](https://ghost.org/help/installing-a-theme/)、[导出](https://ghost.org/help/exports/)

## WordPress

WordPress 的最大价值是长期形成的博客用户心智和迁移格式。WXR 能携带文章、页面、评论、分类、标签等，因此作为首版离线导入格式；但本项目不复刻可执行插件与主题的宽权限模型。[WordPress WXR](https://wordpress.org/documentation/article/tools-export-screen/)、[主题 REST 结构](https://developer.wordpress.org/rest-api/reference/themes/)

## Hugo

Hugo 展示了主题、模板、静态资源、翻译和配置可以作为组合模块组织，也明确采用“模板作者可信、内容作者不可信”的安全前提。本项目借鉴这种主题边界与公开性能，但数据库和管理后台仍是创作权威入口。[Hugo Modules](https://gohugo.io/hugo-modules/introduction/)、[Hugo 功能与安全模型](https://gohugo.io/about/features/)

## WriteFreely

WriteFreely 证明 Go 静态程序配 SQLite 可以形成实际可部署的写作系统。本项目保留这种运行形态，但提供更完整的主题、媒体、SEO、搜索、版本和备份能力，并坚持每个部署只有一个站点。[WriteFreely](https://github.com/writefreely/writefreely)

## Payload

Payload 的配置、后台自定义、版本和插件机制适合作为扩展设计参考；它也说明版本、草稿、自动保存和定时发布应形成一致工作流。本项目最初评估过 Payload，但一核一 GiB 预算使其不适合作为运行内核。[Payload 管理后台](https://payloadcms.com/docs/admin/overview)、[版本](https://payloadcms.com/docs/versions/overview)、[插件](https://payloadcms.com/docs/plugins/overview)

## 最终差异化

本项目的定位不是“更小的 WordPress”或“带后台的 Hugo”，而是：

> 用 Ghost 式出版体验、Hugo 式受限主题、WriteFreely 式部署成本和 Payload 式清晰扩展点，组合成一个数据库驱动、低资源、单站点、唯一站主的现代博客系统。
