# 文档

## 使用

- [安装、配置与运维](usage.md)
- [内容 API v1](api/content-api-v1.md)
- [贡献指南](../CONTRIBUTING.md)

## 开发参考

- [架构](architecture.md)与[实现说明](architecture-implementation.md)
- [数据模型](data-model.md)
- [技术栈](technical-selection.md)
- [产品范围](product-and-roadmap.md)
- [主题与插件](extensions.md)
- [主题开发](development/themes.md)与[澄光主题](development/luminous-editorial-theme-design.md)
- [插件 Host API](development/plugins.md)
- [视图模型](reference/view-models.md)与[事件](reference/events.md)
- [领域词汇](../CONTEXT.md)
- [架构决策（ADR）](adr/README.md)

## 历史记录

[历史文档索引](history.md)收录阶段计划、验收和审计。记录中的版本号、测试结果与待办只代表当时状态，不是当前部署指南或发布保证。

配置默认值以 [`config.example.toml`](../config.example.toml) 和 [`internal/platform/config`](../internal/platform/config/config.go) 为准；数据库结构以[迁移文件](../db/migrations)为准。运行实例的版本与状态请用 `blog version`、`blog status` 查看。
