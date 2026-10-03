# 主题与插件

主题负责页面呈现，插件提供可选功能；S3 和 SMTP 则由应用配置选择适配器。

## 主题

主题包包含 `theme.json`、`templates/`、`assets/` 和许可证。实际模板名称、清单示例和设置类型见[主题开发手册](development/themes.md)；可从 [`themes/example`](../themes/example) 开始。

模板只读取公开视图模型与宿主验证后的 `.Settings`。正文已由核心清洗，普通字符串继续由 `html/template` 转义。主题没有数据库、环境变量、任意文件或命令执行接口。

安装时检查文件数、包与解压大小、路径、符号链接、清单、模板和样例渲染。激活时再次检查目录校验和，失败保持当前主题；内嵌默认主题用于回退。`themes.active` 是权威，`active.json` 是可重建标记。

后台删除主题只是标记移除，保留文件、设置和历史；不能删除当前主题或默认回退主题。重新添加会复核包和设置，不自动激活。支持的[公开视图](reference/view-models.md)及主题操作见[使用手册](usage.md)。

## 插件

插件是编译进二进制的可信 Go 代码，接口为：

```go
type Plugin interface {
    Manifest() Manifest
    Register(*Host) error
}
```

Host API 当前版本为 `1`，支持路由、设置、菜单、事件订阅和持久任务，不提供裸 `*sql.DB`、通用文件系统或进程执行器。这是接口约束，不是隔离任意 Go 代码的沙箱。

公开路由默认位于 `/plugins/{id}/`；评论、Newsletter 和内容 API 使用宿主声明的路由槽。管理路由位于 `/admin/plugins/{id}/`。路由、事件和任务消费均检查启用状态。

| 功能 | 代码 |
|---|---|
| 本地/外部评论，互斥提供方 | `internal/comments` |
| 隐私统计 | `internal/analytics` |
| 只读内容 API | `internal/contentapi` |
| 签名 Webhook | `internal/webhooks` |
| 本地/外部 Newsletter | `internal/notifications` |

可选能力默认关闭。启用失败保留核心站点；停用保留配置、业务数据和待处理任务。后台移除插件会持久化移除状态，重启不会自动恢复；重新添加后仍需启用。移除不删除业务数据。

S3 位于 `internal/media`，SMTP 位于 `internal/notifications`，需要启动配置；后台启用插件不能代替设置这些凭据。详细注册方式见[Host API](development/plugins.md)。

## 事件与任务

当前对外事件为 `ContentPublished.v1` 和 `CommentApproved.v1`，字段见[事件参考](reference/events.md)。未实现的事件名不作为扩展契约。

业务事务写入 `event_outbox` 和派发任务，提交后才调用启用的订阅者。网络和邮件须转为持久任务；处理器应幂等，允许短租约过期后重复执行。插件任务默认最多尝试五次，失败保留供人工处理。

设置与任务 JSON 有 64 KiB 上限。新增字段应向后兼容，改变字段语义须增加版本并保留旧载荷解码。主题和插件的版本、性能与网络限制分别验证，不因功能可用就视为通过发布验收。
