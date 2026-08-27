# 事件版本策略

| 事件 | 版本 | 触发点 | 载荷 |
|---|---:|---|---|
| `ContentPublished.v1` | 1 | 文章或页面事务提交后 | 公开内容 ID、kind、slug |
| `CommentApproved.v1` | 1 | 评论审核提交后 | 公开评论 ID、内容 ID |

事件只在提交成功后派发。外部副作用由 Webhook 等插件转换成持久任务，发布请求不会等待远程网络。Webhook 使用 HMAC-SHA256 签名，并发送 `X-Blog-Event`、`X-Blog-Event-Version`、`X-Blog-Delivery`、`X-Blog-Timestamp` 和 `X-Blog-Signature-256` 头。

新增字段只能向后兼容地追加；改变已有字段含义、删除字段或修改幂等语义时创建新的主版本，并保留旧版本解码器直到所有旧任务完成。
