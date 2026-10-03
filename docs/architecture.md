# 架构

Go 模块化单体。公开页面、管理后台、任务和 CLI 使用同一套服务与 SQLite 数据库。

```text
浏览器 / Feed / API
        │
      Caddy
        │
    Go 应用 ─── 可选 S3 / SMTP / Newsletter / Webhook
        │
    SQLite WAL + 持久数据卷
```

默认 Compose 包含一次性 `data-init`、常驻 `app` 和 `caddy`，数据卷为 `blog_data`、`caddy_data`、`caddy_config`。应用数据位于 `/data/site`。应用非 root、根文件系统只读，限制为 0.85 CPU / 256 MiB，Go 堆软上限为 192 MiB。

## 模块与依赖

核心模块为 identity、publishing、organization、media、presentation、discovery、operations、extensions；配置、数据库和通用 HTTP 工具位于 `internal/platform`。

官方可选能力位于 `internal/comments`、`analytics`、`contentapi`、`notifications`、`webhooks`，由 `internal/app` 组装。没有顶层 `plugins/` 代码目录。后台资源位于 `web/admin`，默认主题位于 `themes/default`，均嵌入二进制。

```text
HTTP / CLI → 应用服务 → Repository → SQLite
                     → 提交后事件与持久任务
主题 → 公开视图模型
插件 → extensions.Host
```

不建立全项目横向的 controllers/services/repositories 目录。跨模块协作使用服务接口、专用查询或版本化事件；插件 Host 不暴露裸数据库、任意文件系统或进程执行能力。

## 数据与事务

Markdown 和不可变修订是正文来源。公开查询只读取已发布修订，不能读取草稿或编辑快照。公共 URL 使用 slug 或稳定公共 ID，不能暴露 SQLite 行号。

业务写入在短事务中保存状态、修订、媒体引用、审计和事件；影响公开内容的写入同时增加 `system_state.render_epoch`。已发布内容改 slug 时保留旧路径并创建重定向。网络、邮件、Webhook、图片处理和 Markdown 渲染在事务外执行。

搜索、页面缓存、taxonomy 投影和媒体变体可重建。统计只保存聚合结果，不能据此重建原始访问记录。

## 页面、任务与扩展

公开页面和后台完整服务端渲染，JavaScript 用于渐进增强。公开页面缓存使用 `render_epoch` 失效；错误、后台、预览和搜索响应不进入页面缓存。

后台循环以有限批次处理搜索同步、定时发布、清理和持久任务。任务有幂等键、短租约、有限重试，失败可从后台查看。进程重启后继续处理数据库中的工作，不依赖内存定时器。

主题是受限 `html/template` 和静态资源包；坏包不能替换当前主题。插件是可信编译期 Go 代码，默认关闭，停用或移除保留数据。S3 和 SMTP 是应用组装的适配器，不是后台可启停的插件。

启动流程、模块路径、缓存和外部网络限制见[实现说明](architecture-implementation.md)。数据库关系见[数据模型](data-model.md)，设计理由见[ADR](adr/README.md)。
