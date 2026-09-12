# 项目文档索引

本文档目录区分“当前实现说明”“设计约束”“开发计划”和“历史验收记录”。阅读当前代码时，优先使用当前实现说明；计划和进度文档用于追踪为什么这样设计以及哪些验收仍需补做。

## 从这里开始

- [项目使用与运维手册](./usage.md)：安装、启动、初始化、后台使用、CLI、备份恢复、导入导出、扩展启用和故障排查。
- [实现级架构与技术细节](./architecture-implementation.md)：部署拓扑、启动流程、模块依赖、数据库、发布、缓存、任务、主题、插件和性能边界。
- [总体架构](./architecture.md)：架构原则、模块职责和关键不变量。
- [数据模型](./data-model.md)：权威数据、表之间的关系、索引和数据保留规则。
- [领域词汇表](../CONTEXT.md)：项目中 Site、Owner、Article、Page、Revision、Snapshot 等术语的定义。

## 使用和 API

- [只读 Content API v1](./api/content-api-v1.md)
- [主题开发手册](./development/themes.md)
- [插件 Host API 与事件策略](./development/plugins.md)
- [主题与插件契约总览](./extensions.md)
- [视图模型参考](./reference/view-models.md)
- [事件版本策略](./reference/events.md)

## 设计决策和安全

- [功能范围与交付路线](./product-and-roadmap.md)
- [技术选型](./technical-selection.md)
- [博客界面重构计划](./ui-refactor-plan.md)
- [架构决策索引](./adr/README.md)
- [阶段一生产安全审计](./security/phase-one-production-hardening.md)
- [阶段三安全、依赖与恢复审计](./security/stage-3-audit.md)

## 实施计划和验收记录

- [项目现状评估与改进路线图](./project-assessment-and-improvement-roadmap.md)
- [改进阶段一：生产安全与一致性](./progress/phase-one-production-hardening.md)
- [改进阶段二：内容与扩展契约](./progress/phase-two-content-and-extension-contract.md)
- [改进阶段三：规模与运维](./progress/phase-three-scale-and-operations.md)
- [阶段三性能优化报告](./progress/phase-three-performance-implementation.md)
- [改进阶段四：产品与公共能力](./progress/phase-four-product-and-public-capabilities.md)
- [阶段四设计计划](./development/phase-four-product-and-public-capabilities-plan.md)

## 文档阅读约定

- `docs/development/` 主要记录实施计划、边界和验收要求；其中“计划”不等于已经实现。
- `docs/progress/` 记录某次实施或测试时的结果，带有日期和迁移版本，旧记录是历史证据，不覆盖当前运行状态。
- ADR 的 `status: accepted` 是当前设计约束；`superseded` 只保留历史背景。
- 当前数据库迁移版本、任务和备份状态以 `blog status`、`blog migrate` 和实际健康检查为准，不以旧进度文档中的版本号为准。
- 主题插件和 `.playwright-results/` 等工作区运行产物不属于默认主题核心实现文档的验收范围。
