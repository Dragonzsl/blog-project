# 架构决策索引

ADR 保留做出决定时的原始上下文。被取代的记录不删除，以便理解决定如何演进；当前设计只服从状态为 `accepted` 的记录及其后继决定。

## 产品与范围

- [0001 单站点个人出版边界](./0001-single-site-personal-publishing.md) — 已被 0014 取代
- [0013 不建立读者账户与付费会员](./0013-no-reader-accounts-or-paid-memberships.md) — 已被 0014 取代
- [0014 唯一站主账户](./0014-single-owner-account.md)
- [0016 文章与页面](./0016-articles-and-pages-only.md)
- [0022 单一站点主要语言](./0022-single-primary-language.md)
- [0030 首版容量边界](./0030-first-version-capacity-envelope.md)
- [0035 可复用开源单站点产品](./0035-reusable-open-source-single-site-product.md)
- [0036 Apache-2.0](./0036-apache-2-license.md)
- [0037 三个纵向交付阶段](./0037-vertical-delivery-toward-version-one.md)

## 架构与运行

- [0002 数据库是内容权威来源](./0002-database-as-content-source-of-truth.md)
- [0003 模块化单体](./0003-modular-monolith.md)
- [0004 容器化自托管优先](./0004-containerized-self-hosting-first.md)
- [0005 一核一 GiB 资源预算](./0005-one-core-one-gib-resource-budget.md)
- [0006 Go 与 SQLite CMS 核心](./0006-go-and-sqlite-cms-core.md)
- [0011 公开页面可缓存快照](./0011-cacheable-public-page-snapshots.md)
- [0012 服务端管理后台](./0012-server-rendered-admin-with-interactive-islands.md)
- [0023 SQLite FTS5 搜索](./0023-sqlite-fts5-search.md)
- [0027 完整备份与 CLI 恢复](./0027-verifiable-full-backups-and-cli-restore.md)
- [0028 外部升级与向前迁移](./0028-external-upgrades-and-forward-only-migrations.md)
- [0031 性能发布门槛](./0031-performance-budgets-are-release-gates.md)
- [0034 默认零遥测](./0034-no-telemetry-by-default.md)

## 内容与发布

- [0007 Markdown 正文格式](./0007-markdown-as-canonical-body-format.md)
- [0017 发布生命周期](./0017-content-publication-lifecycle.md)
- [0018 编辑快照与不可变版本](./0018-editing-snapshots-and-immutable-revisions.md)
- [0019 单分类与多标签](./0019-one-category-and-multiple-tags.md)
- [0020 固定永久链接](./0020-fixed-stable-permalinks.md)
- [0024 媒体与存储适配器](./0024-local-first-media-with-storage-adapter.md)
- [0025 核心 SEO、重定向、站点地图与订阅源](./0025-core-seo-redirects-sitemap-and-feed.md)
- [0033 开放归档与离线导入器](./0033-portable-archive-and-offline-importers.md)

## 主题与插件

- [0008 分离主题与插件](./0008-separate-themes-from-plugins.md)
- [0009 可信编译期插件](./0009-trusted-compiled-plugins.md)
- [0010 运行时 Go 模板主题](./0010-runtime-go-template-themes.md)
- [0021 可选本地评论插件](./0021-optional-local-comments-plugin.md)
- [0026 可选本地统计插件](./0026-optional-privacy-first-local-analytics.md)
- [0029 现代编辑式默认主题](./0029-modern-editorial-default-theme.md)
- [0032 可选只读 API 与 Webhook](./0032-optional-read-only-content-api-and-webhooks.md)

## 安全

- [0015 密码与强制 TOTP](./0015-owner-password-and-mandatory-totp.md)
