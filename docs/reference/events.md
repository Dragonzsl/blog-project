# 事件版本策略

| 事件 | 版本 | 生产者 | 事务内登记 | 公开字段 |
|---|---:|---|---|---|
| `ContentPublished.v1` | 1 | Publishing 手动发布、定时发布 | 内容状态、修订、路径、审计和事件任务同一事务 | 事件 ID、内容公开 ID；payload 为 `kind`、`slug` |
| `CommentApproved.v1` | 1 | Comments 审核通过 | 评论状态、审核时间、审计和事件任务同一事务 | 事件 ID、评论公开 ID；payload 为内容公开 ID |

事件先写入 `event_outbox`，再以 `core:event_dispatch` 任务持久化；只有业务事务提交后才派发。任务载荷只引用事件 ID，事件派发失败按统一队列的短租约、退避和五次上限处理。外部副作用由 Webhook 等插件转换成持久任务，发布请求不会等待远程网络。相同事件 ID 使用稳定任务幂等键；Webhook delivery identity 也从事件 ID 派生，允许接收方去重。Webhook 使用 HMAC-SHA256 签名，并发送 `X-Blog-Event`、`X-Blog-Event-Version`、`X-Blog-Delivery`、`X-Blog-Timestamp` 和 `X-Blog-Signature-256` 头。

插件被禁用时不会接收新的事件或任务，已有任务保留；重新启用后可继续消费。若事件没有启用订阅者，dispatcher 会安全完成该事件任务，不产生远程副作用。

新增字段只能向后兼容地追加；改变已有字段含义、删除字段或修改幂等语义时创建新的主版本，并保留旧版本解码器直到所有旧任务完成。
