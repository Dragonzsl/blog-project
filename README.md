# 个人博客系统

这是一个面向公开复用的单站点自托管博客系统设计。当前仓库处于**设计完成、尚未开始实现**阶段。

核心方向：Go 模块化单体、SQLite、Markdown、服务端渲染、可上传主题、可信编译期插件，以及可在 1 核 1 GiB VPS 上稳定运行的硬性资源预算。

## 设计文档

- [领域词汇表](./CONTEXT.md)
- [技术选型](./docs/technical-selection.md)
- [总体架构](./docs/architecture.md)
- [数据模型](./docs/data-model.md)
- [主题与插件契约](./docs/extensions.md)
- [功能范围与交付路线](./docs/product-and-roadmap.md)
- [主流博客系统对照](./docs/research/popular-systems.md)
- [架构决策索引](./docs/adr/README.md)

## 一句话架构

公开请求由 Caddy 终止 TLS 后进入一个 Go 应用；Go 应用同时承载公开页面、管理后台、任务与 CLI 模式，使用同进程 SQLite 和本地持久卷，S3、邮件及外部集成均为可选适配器。

## 当前约束

- 每次部署只有一个站点和一个站主，不做角色、多租户或读者账户。
- 文章与页面正文以数据库中的 Markdown 为权威版本。
- 公开页面优先走可缓存快照，动态能力走独立接口。
- 主题可上传但不能执行服务端代码；插件随程序编译并按配置启停。
- 所有 1.0 功能按三个可运行的纵向阶段交付。
