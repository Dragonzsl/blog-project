---
status: accepted
---

# 阶段二内容与扩展契约

## 背景

阶段一已经建立 SQLite 短事务、提交后任务和可选插件的运行基础，但封面、主题配置和扩展注册仍存在跨模块表达不一致的窗口。阶段二需要在不改变单站点、单 Owner、Markdown 权威正文和服务端渲染边界的前提下闭合这些契约。

## 决策

1. 封面是媒体引用，不是任意 URL。`contents.cover_media_id` 表示当前编辑状态，`content_revisions.cover_media_public_id` 与 `cover_snapshot_version` 表示不可变修订快照；版本 0 保留迁移前的未知历史，版本 1 表示已记录。正文和封面引用在同一短事务中重建，删除保护使用二者并集。
2. 公开封面只从已发布修订解析为安全媒体视图。视图不包含 SQLite 行号、对象键、哈希或路径；媒体缺失时公开页面降级为无图。归档和离线导入使用稳定公共 ID，缺失媒体报告冲突而不生成失效 URL。
3. 主题设置由 manifest 的 `settingsVersion` 和受限 schema 管理。主题模板只能得到校验后的 `.Settings` 副本；secret 设置完全隐藏，media 设置由宿主解析为展示视图。主题安装保存 ZIP 与确定性目录 checksum，并通过固定页面 fixture 后才允许激活。
4. `themes.active` 是跨重启权威；`active.json` 只是可修复缓存。激活历史保存前一主题和操作状态，使回退保留安装包、设置与审计数据。启动 reconcile 按数据库状态复核目录并修复 marker，失败使用嵌入式默认主题。
5. 插件公开路由默认使用 `/plugins/{id}/` 命名空间，兼容旧公共 URL 只能使用宿主扩展槽；管理路由自动位于 `/admin/plugins/{id}/`。启用过程先在临时注册表登记，成功后一次性合并。
6. 插件设置、事件和任务显式版本化并有大小边界。设置迁移失败保持禁用并保留旧 JSON；事件订阅按 `.vN` 匹配；任务使用插件命名空间、payload 版本、幂等键和有限重试。禁用插件不消费任务/事件，也不删除配置和数据。

## 影响

阶段二新增内容封面和主题激活迁移，并将已有官方评论、Newsletter、内容 API 路由迁移到宿主扩展槽。旧主题可继续使用默认目录/状态模板回退；旧插件 API 的基本注册方式保持兼容，但公开路由、任务大小和设置秘密处理更严格。未引入动态插件、外部市场、读者账户或新的基础设施。

## 验证

实现与验收记录见 [阶段二实施计划](../development/phase-two-content-and-extension-contract-plan.md) 和 [阶段二进度记录](../progress/phase-two-content-and-extension-contract.md)。
