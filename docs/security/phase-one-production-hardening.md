# 改进阶段一安全基线

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

本文件记录路线图“生产安全与一致性”阶段的安全边界，适用于 2026-09-02 之后的实现。

## 客户端身份

`server.trusted_proxy_cidrs` / `BLOG_TRUSTED_PROXY_CIDRS` 默认为空。应用只在直接 TCP 对端命中明确的代理 CIDR 时解析 `X-Forwarded-For`，并从右向左选取第一个非可信地址；重复头、空项、非法地址和超长链全部回退到直接对端。`Forwarded` 等其他头不生效。

同一个 resolver 用于登录失败限流、评论限流、Newsletter 重复提交指纹和 analytics 访客 HMAC。应用不把原始地址写入数据库。

## 公开写入

评论和 Newsletter 通过 `publicwrite.Guard` 使用服务端密钥计算幂等键/请求指纹摘要。显式幂等键绑定请求指纹，换请求体返回冲突；无幂等键的短期重复被拒绝或以通用 `202 Accepted` 响应吸收。辅助表的响应摘要、TTL 和每轮清理均有上限。

Newsletter 订阅使用 pending → confirmation token → active 流程。确认和退订 token 按用途隔离、一次性消费并有过期时间；公开接口不接受邮箱地址直接退订，也不通过响应区分邮箱是否已存在。token 原文不进入日志；用于异步发送确认邮件的持久数据是受密钥保护的密文。

## 外发网络

Webhook、外部 Newsletter 和 SMTP 经过 `netguard`：只允许绝对 HTTP/HTTPS 或合法 SMTP 端口，设置连接/响应/总超时，禁止自动重定向，并在每次连接解析后拒绝 loopback、未指定、私网、链路本地和多播地址。重定向不会被跟随，因此即使首个地址返回指向内网的 3xx，也不会产生第二跳请求。

网络错误只保留有限、脱敏的任务错误；Authorization、token、邮箱、原始请求体和完整隐私地址不进入日志、审计或运维页。

## 事务与恢复

发布和评论审核在业务事务内写入审计、事件 outbox 和通用任务；提交前失败会一起回滚，提交后退出由 jobs 租约恢复。通知、Webhook 和 Newsletter provider 调用在事务外执行，统一使用有限退避和最大五次尝试。人工重试复用原 job 行并写 `operations.task.retried` 审计。

禁用插件不会删除配置、数据、投递记录或任务；路由、事件和任务消费均受 enabled 状态保护。`/admin/operations/tasks` 只展示 kind、状态、尝试次数、时间和脱敏错误，不展示任务 payload。

## 运维要求

- 正式部署必须把 `BLOG_TRUSTED_PROXY_CIDRS` 改成真实入口代理出口网段；不使用 `0.0.0.0/0` 或全部私网网段作为兜底。
- 外部适配器默认关闭；测试可注入 transport，但生产构造不提供绕过 `netguard` 的配置开关。
- 升级后检查 `/livez`、`/readyz`、`blog status` 和任务页；确认迁移版本为 13，失败任务有明确处置。
- 本阶段只保证 at-least-once 触发语义；Webhook 接收端和邮件接收端仍需按 delivery/task identity 自行去重。
