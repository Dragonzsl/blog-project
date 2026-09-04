# 改进阶段一实施计划：生产安全与一致性

> 上位文档：[项目现状评估与改进路线图](../project-assessment-and-improvement-roadmap.md)
>
> 计划状态：阶段完成，工程门禁通过（2026-09-02）；详细证据见 [阶段一实施记录](../progress/phase-one-production-hardening.md)
>
> 计划基线：2026-09-02；基线数据库迁移版本为 10，实施后版本为 13

## 1. 计划说明

项目已有一份历史性的“阶段一实施记录”：[docs/progress/stage-1.md](../progress/stage-1.md)。那份文档记录的是 1.0 核心闭环的首次建设，本计划中的“改进阶段一”指路线图中的“生产安全与一致性”阶段，两者不是同一个阶段。

本阶段不重做文章、主题或数据库基础设施，而是补齐已经暴露出的跨模块契约：

1. 反向代理之后仍能正确识别客户端，并且不接受伪造的转发头。
2. 评论和 Newsletter 等公开写入具备限流、幂等、反重放和隐私安全边界。
3. 手动发布、定时发布和评论审核的事件不因进程退出而丢失。
4. Webhook、Newsletter 和通知等外部副作用在事务外执行，失败可重试、可终止、可观察。
5. 所有新增能力保持单站点/单 Owner、SQLite、短事务和低资源预算约束。

## 2. 阶段目标与出口

### 2.1 阶段目标

| 目标 | 可交付结果 |
| --- | --- |
| 客户端身份 | 新增可信代理 CIDR 配置和统一客户端地址解析器，Identity、Comments、Analytics 共用同一语义 |
| 公开写入 | 评论和 Newsletter 使用统一的请求去重/幂等边界；不通过邮箱地址直接执行退订 |
| 网络安全 | HTTP 外发适配器统一超时、无重定向、私网地址拒绝、DNS 解析重检和有界响应体 |
| 事件可靠性 | 事件在业务事务内登记为持久任务，提交后异步派发；手动和定时发布使用相同事件语义 |
| 任务可靠性 | 现有 `jobs` 队列抽象统一租约、退避、最大尝试次数、幂等和人工重试 |
| 运维可见性 | 管理台可查看 pending/running/failed 任务、最近错误和下一次重试时间，并支持安全重试 |

### 2.2 阶段出口

只有以下条件全部满足，才可将本阶段标记完成：

- Compose/Caddy 部署下，不同真实客户端不会因为共享代理地址被合并；直连请求或伪造 `X-Forwarded-For` 不能绕过登录和公开写入限流。
- 相同幂等键的并发提交最多产生一个业务结果；相同请求指纹的无幂等键重放不会产生重复评论或重复订阅操作。
- 手动发布和定时发布各产生一条可追踪的 `ContentPublished` 事件；进程在事务提交后立即退出，下一次启动仍能处理事件。
- 外部请求失败按有限策略重试，达到上限进入 `failed`；敏感信息不会进入日志、审计上下文或任务错误。
- 任务、备份和健康状态至少在现有状态查询或新增运维页中可见；关键失败不再只能通过日志猜测。
- 空数据库、从迁移版本 10 升级、重复启动、租约过期和恢复备份均有自动化证据。

## 3. 当前基线

| 能力 | 当前实现 | 本阶段处理方式 |
| --- | --- | --- |
| 代理识别 | `internal/identity/http.go` 和 `internal/analytics/service.go` 直接从 `RemoteAddr` 推断客户端；评论限流在 `internal/comments/http.go` 中也使用 `RemoteAddr` | 新增平台级解析器，调用方不再自行解析请求头 |
| 登录限流 | `identity` 有有界内存限流器，登录 key 由地址和用户名组成 | 仅替换地址来源，保留已有失败次数和 Argon2 并发保护 |
| 评论限流 | 每个连接地址约 5 次/分钟，内存表上限 1024 | 接入统一客户端身份、幂等和持久短期去重；不在本阶段做复杂反垃圾模型 |
| Newsletter | 本地提供方直接把订阅设为 active；退订只需邮箱；外部提供方在 HTTP 请求中同步调用 | 增加确认/退订 token、泛化响应和持久任务；保留适配器边界 |
| Plugin jobs | `jobs` 表已有幂等键、`pending/running/succeeded/failed`、30 秒租约和最多 5 次尝试；Webhook 已使用它 | 把队列操作收敛到 Operations 窄接口，增加核心事件和通知任务 |
| 通知 outbox | `notification_outbox` 有 pending/sent/failed，但失败后立即终止，没有租约和退避 | 改为引用统一任务队列，增加失败重试和人工重试 |
| 发布事件 | 手动发布提交后直接调用 `Registry.Dispatch`；定时发布只更新内容和审计，不走相同事件路径 | 事务内登记事件任务，移除业务服务中的直接派发 |
| Webhook | 已有独立投递记录、HMAC 签名、超时和有限重试；端点只做 URL 语法校验 | 增加事件幂等、私网地址/DNS 防护，并复用统一队列 |
| 生命周期 | `internal/app/app.go` 的一个循环同时处理定时发布、搜索同步、插件任务和通知 | 增加核心任务处理，保持单进程、有界批量和无额外常驻服务 |

## 4. 不变约束

实施时必须保持以下约束；若实现方案需要改变其中任何一项，应先补充 ADR 并暂停编码：

- 不引入多用户、角色、读者账户、多租户或协作者模型。
- SQLite 仍是权威数据源；每个业务写入使用一个短事务。
- Markdown 渲染、网络请求、邮件发送、Webhook 投递和其他慢操作不能在业务写事务中执行。
- 外部副作用采用“至少一次触发 + 目标端幂等”的语义，不宣称网络世界的绝对 exactly-once。
- 公共事件只携带稳定公开 ID 和必要的小字段，不携带正文、邮箱、会话、密钥或内部 SQLite 行号。
- 所有任务、限流表、响应体、错误文本和清理动作都有上限。
- 关闭可选插件时保留其数据和配置；插件路由、事件和任务必须停止接收新工作。

## 5. 目标结构

```text
HTTP request
    │
    ├─ trusted client-IP resolver ──> login / comment / analytics
    ├─ public-write guard ──────────> rate + idempotency + fingerprint
    └─ domain service transaction
           │
           ├─ business data + audit + durable event/task record
           └─ commit
                   │
                   └─ bounded lifecycle worker
                         ├─ event dispatch
                         ├─ notification send
                         ├─ webhook delivery
                         └─ newsletter provider call
```

现有 `jobs` 表继续作为唯一的通用执行队列。不要为事件、Webhook、通知和 Newsletter 分别再创建一套 lease/retry worker；业务模块可以保留自己的投递记录表，但执行调度必须走同一个 Operations 队列接口。

## 6. 实施任务

### H0：契约冻结与基线记录

**目的**：在修改公共安全和事件语义前，先固定当前行为和兼容边界。

**工作项**：

- 盘点所有 `RemoteAddr`、`X-Forwarded-For`、`Forwarded`、公开 `POST` 和外部网络调用点，形成代码清单。
- 盘点所有事件生产者、订阅者、任务 kind、幂等键和现有状态字段。
- 对照 `docs/extensions.md`、`docs/reference/events.md` 与实际代码，列出“文档已声明但代码尚未实现”的事件，不在本阶段默默扩大事件范围。
- 确认已有 Webhook 是否存在外部消费者。如果已有消费者依赖当前载荷，改变字段语义时必须新建事件主版本；如果尚无稳定消费者，则修正文档和 v1 实现，并记录兼容决定。
- 记录迁移版本 10 的空库初始化、升级库、备份恢复和重启作为基线测试。

**产物**：

- 一份事件/任务矩阵，至少包含生产者、事务边界、载荷版本、幂等键、订阅者和失败语义。
- 一份外部端点清单，包含 Webhook、Newsletter、SMTP 的超时、重定向、私网访问和错误分类。
- 若队列或事件载荷语义发生变化，新增 ADR；只增加实现而不改变架构时更新现有参考文档即可。

**依赖**：无。H0 完成后才能进入 H1、H2、H4。

### H1：可信代理与客户端身份

#### H1.1 配置

在 `internal/platform/config` 增加启动配置，建议使用以下命名：

```toml
[server]
trusted_proxy_cidrs = []
```

同时支持：

```text
BLOG_TRUSTED_PROXY_CIDRS=10.20.0.0/24,172.30.0.2/32
```

规则：

- 默认空列表，空列表表示不信任任何转发头。
- 不自动信任所有私有网段；Compose、反代或平台部署文档必须要求操作者填入实际应用入口网段/IP。
- 启动时校验 CIDR 数量、格式和地址族；非法配置拒绝启动，不在运行中悄悄放宽策略。
- 客户端 IP 配置属于基础设施配置，修改后重启生效，不进入站点导出归档。

#### H1.2 解析算法

新增窄模块，建议路径为 `internal/platform/clientip`。使用 `net/netip` 做规范化，不把完整的 `host:port` 传给业务模块。

1. 从 `RemoteAddr` 提取直接对端地址；解析失败时返回一个不可伪造的固定“未知客户端”值，不信任任意头部。
2. 只有直接对端地址命中可信代理 CIDR 时，才解析 `X-Forwarded-For`。
3. 从转发链右向左检查，选择第一个不属于可信代理网段的合法地址。
4. 发现非法地址、空项、端口异常或超过合理链长度时，回退到直接对端地址，并记录不含地址内容的受限诊断信息。
5. 不同时解析多个来源头；本阶段只支持部署文档约定的 `X-Forwarded-For`，`Forwarded` 等其他头不自动生效。

#### H1.3 接入点

- `internal/identity/http.go`：登录限流 key 使用 resolver 结果；删除本地 `remoteAddress` 解析逻辑。
- `internal/comments/http.go`：匿名评论限流使用 resolver 结果，保留条目上限和定期淘汰。
- `internal/analytics/service.go`：访客 HMAC 输入使用 resolver 结果和 User-Agent；原始地址仍不写数据库。
- 如访问日志未来需要地址，只记录截断/哈希后的值，不能直接复用隐私统计原始输入。
- 更新 `config.example.toml`、`.env.example`、Compose/反代说明和安全文档，明确可信网段来源。

#### H1.4 测试

- IPv4、IPv6、带端口地址、IPv4-mapped IPv6、空值和非法值。
- 未配置可信代理时，伪造 `X-Forwarded-For` 无效。
- 可信代理链能得到最左侧真实客户端；中间代理地址不会被当作客户端。
- 伪造超长链、非法链和重复头不会绕过限流。
- 登录、评论和 analytics 在同一请求构造下使用同一客户端身份。
- Caddy/Compose 集成测试覆盖真实反代链，而不是只在 `httptest` 直接请求应用。

### H2：公开写入保护与幂等

#### H2.1 持久去重边界

新增下一编号迁移（建议 `00011_phase1_write_safety.sql`），只做加法，不修改已有迁移。建议包含两个有界表：

- `request_idempotencies`：保存作用域、幂等键摘要、请求指纹摘要、结果状态、有限大小的安全响应摘要、创建/过期/更新时间。唯一键为 `(scope, key_hash)`。
- `public_write_fingerprints`：保存作用域、业务指纹摘要、创建时间和过期时间，用于没有显式幂等键的浏览器重复提交保护。

摘要必须使用服务端密钥计算，不能用裸 SHA-256 直接保存可猜测的邮箱、IP 或内容组合。表中不得存储完整邮箱、原始 IP、评论正文或请求体。过期清理每轮最多删除固定数量，并在生命周期中低频执行。

#### H2.2 统一语义

- `Idempotency-Key` 允许公开写客户端显式提供；长度、字符集和作用域均有限制。
- 同一作用域、同一幂等键、同一请求指纹：返回首次安全结果，不重复写入。
- 同一幂等键但请求指纹不同：返回 `409 Conflict`，不覆盖首次结果。
- 没有幂等键但指纹在短窗口内重复：评论返回明确的重复提交错误；Newsletter 返回不泄露账号存在性的通用 `202 Accepted`。
- 幂等结果不保存完整响应正文，只保存必要的状态和资源公开 ID；响应体设置固定上限。
- 幂等键过期后可以重新使用，但旧记录必须先过期/清理，不能无限增长。

#### H2.3 评论改造

- 保留现有评论约 5 次/分钟的基础限流作为默认值，限流 key 改为 H1 resolver 结果；条目仍需有界。
- 在 Markdown 渲染、URL 校验和输入规范化完成后计算指纹；正文只进入 keyed digest，不进入去重表。
- 将评论写入、审计和必要的待审核通知登记放进一个短事务；通知不在事务中发邮件。
- 并发相同幂等键必须通过数据库唯一约束收敛，而不是依赖 Go mutex。
- 错误响应不回显邮箱、完整指纹、SQL 错误或内部 ID；管理员查看评论仍遵循现有脱敏规则。

#### H2.4 Newsletter 改造

数据库已有 `newsletter_subscribers.status` 的 `pending/active/unsubscribed` 状态，本阶段利用它而不是新增账户体系。

- 订阅请求只创建/更新 `pending` 意图并生成一次性确认 token；响应统一为“如果地址有效，确认邮件将发送”，不透露邮箱是否已存在。
- token 只保存 keyed hash、用途、订阅者引用、过期时间和消费时间；原始 token 只出现在确认链接中，不写日志或数据库。
- 确认后才把本地订阅设为 `active`；重复确认是幂等的，过期或错误 token 只返回通用失败页面。
- 退订只接受签名/随机 token，不再以“提交邮箱地址”作为授权依据；退订操作幂等且可审计。
- 确认/退订链接使用独立用途、有效期和 token 版本，避免确认 token 被用于退订或跨环境重放。
- 外部提供方不得在订阅 HTTP 请求中同步访问网络；确认后的 provider 操作进入统一任务队列。
- 保持 `NewsletterAdapter` 窄接口；本阶段不引入群发、模板编辑、会员分组或营销自动化。

#### H2.5 测试

- 同一幂等键并发 20 次只有一个插入结果。
- 同一幂等键换请求体返回 409，原结果不改变。
- 进程重启后仍能识别短期重复指纹；过期清理不会删除未过期记录。
- 评论重复、跨文章重复、不同客户端相同正文分别符合预期。
- Newsletter 不存在/已存在/已退订邮箱返回相同外部响应语义。
- 确认 token 一次性、过期、用途错误和重放均有测试；退订 token 不能确认订阅。

### H3：外部网络访问安全

新增窄适配器，建议路径为 `internal/platform/netguard`，不让每个插件自行实现 DNS 和 HTTP 策略。

#### H3.1 HTTP 客户端策略

- 仅允许绝对 `http`/`https` URL，拒绝用户信息、超长 URL、空主机和不支持 scheme。
- 禁止自动重定向；重定向响应作为明确失败处理，不跟随到第二个地址。
- 设置连接、TLS、响应头和总请求超时；响应体只读取固定上限。
- 每次建立连接时解析目标地址并拒绝 loopback、unspecified、private、link-local、multicast、IPv4-mapped 私网和明显的本地主机名。
- 不在启动时依赖一次 DNS 解析结果；连接阶段重新解析并对最终 IP 做检查，降低 DNS rebinding 风险。
- 将永久配置错误与临时网络错误区分，永久错误不进行无意义重试。
- 失败日志只记录适配器、状态类别和尝试次数，不记录 Authorization、token、请求体或完整敏感 URL 查询参数。

#### H3.2 接入点

- `internal/webhooks/plugin.go`：保留现有 HMAC/超时/无重定向，改用 netguard；投递记录采用稳定事件幂等键。
- `internal/notifications/newsletter.go`：补上无重定向、netguard 和有界响应体；所有调用通过任务 worker 执行。
- `internal/notifications/mail.go`：审查 SMTP 主机解析、端口范围、连接超时和错误脱敏；至少禁止明显的 loopback/未指定地址。
- 测试 HTTP server 通过注入测试 transport 或显式测试 allowlist 访问，不增加生产配置绕过私网检查的开关。

#### H3.3 测试

- 公网域名、loopback、私网、link-local、IPv6 本地地址、DNS 多地址和解析失败。
- 目标先返回 302 再指向私网时必须失败。
- 超时、非 2xx、过大响应体和 TLS 错误的错误类别和重试策略正确。
- 测试中验证请求头不会泄露 token，任务错误不会保存响应正文。

### H4：统一任务队列和事件 outbox

#### H4.1 队列抽象

现有 `jobs` 表已经具备可复用字段，建议将读写和 claim/retry 逻辑收敛到 `internal/operations` 的窄接口，例如：

```go
type TaskQueue interface {
    Enqueue(ctx context.Context, task Task) error
    EnqueueTx(ctx context.Context, tx *sql.Tx, task Task) error
    ProcessOne(ctx context.Context, handler Handler) (bool, error)
    Retry(ctx context.Context, id int64) error
}
```

实际接口可以避免暴露 `*sql.Tx`，但必须能表达事务内入队。插件 Host、通知、Webhook 和 Publishing 只能拿到能力接口，不能获得裸数据库或通用文件系统。

建议新增/固定任务 kind：

| kind | 载荷 | 幂等键 | 处理者 |
| --- | --- | --- | --- |
| `core:event_dispatch` | 事件 ID、名称、版本、公开对象 ID、有限 payload | `event:<event_id>` | Core event dispatcher |
| `core:notification_send` | notification outbox ID | `notification:<id>` | Notifications |
| `plugin:webhooks.signed:deliver` | delivery ID | `webhook:<delivery_id>` | Webhooks |
| `plugin:newsletter.*:sync` | subscriber ID、操作和版本 | `newsletter:<subscriber>:<operation>:<version>` | Newsletter |

队列规则：

- 默认最大尝试次数沿用 5 次；Webhook 可以使用更低的插件级上限，但不能超过全局上限。
- 租约、基础退避、最大退避和每轮处理条数可配置且有上下限；默认继续使用短租约和低频生命周期循环。
- 过期 `running` 任务回到 `pending`；达到上限后进入 `failed`，不再忙循环。
- 人工重试复用原任务 ID，清除下一次执行时间和上一次错误，并留下审计记录；不能通过新建无限重复任务绕过幂等键。
- 错误文本截断、分类并脱敏；日志不记录载荷正文。

#### H4.2 事务内登记事件

为 Publishing 和 Comments 注入窄 `EventRecorder`/`TaskQueue` 端口，使它们可以在已有业务事务中登记任务。

必须改造的路径：

| 路径 | 事务内动作 | 必须触发的事件 |
| --- | --- | --- |
| 手动文章发布 | 内容状态、公开修订、路径、`render_epoch`、审计和事件任务 | `ContentPublished.v1` |
| 手动页面发布 | 同上 | `ContentPublished.v1` |
| 定时文章/页面发布 | 到期条件更新、公开修订、路径、`render_epoch`、审计和事件任务 | 与手动发布相同的 `ContentPublished.v1` |
| 评论审核通过 | 评论状态、审核时间、审计和事件任务 | `CommentApproved.v1` |
| 待审核评论通知 | 通知 outbox 记录和发送任务 | `core:notification_send` |

事件 ID 必须在事务中生成并持久化于任务幂等键中；同一业务事务重试时不能生成多个对外事件。事件 payload 使用公开 ID、kind、published slug 等有限字段，不使用 `content_id` 等内部行号作为跨实例契约。

如果现有 `CommentApproved.v1` 或 Webhook 已有消费者依赖内部字段，先按 H0 的兼容决策保留 v1 并新增 v2；禁止无记录地改变已发送事件的字段含义。

提交之后由 dispatcher 调用 `Registry.Dispatch`。Publishing 和 Comments 不再直接忽略 `Dispatch` 错误；事件任务失败必须进入统一 retry/failed 状态。

#### H4.3 Webhook 幂等

- Webhook 投递记录增加事件 ID/订阅者作用域或等价唯一约束。
- 同一事件重派时复用同一个 delivery identity，不因每次 retry 生成新的随机 delivery。
- “创建投递记录”和“入队”必须通过统一队列的事务接口原子完成，避免出现只有 delivery 没有 job 的孤儿状态。
- 远程 endpoint 返回成功后，投递记录和 job 状态按现有审计语义收敛；重复收到同一事件时目标端应可用 `X-Blog-Delivery` 去重。

#### H4.4 通知重试

- `notification_outbox` 继续保存邮件业务数据，但发送任务只引用其 ID。
- 失败不立即永久 `failed`；按退避更新 `available_at`，达到上限才进入 `failed`。
- 增加租约或由统一 job lease 负责并发控制，避免同一邮件被两个生命周期 tick 同时发送。
- SMTP 发送成功但应用在写回状态前退出时，允许 at-least-once 重发；邮件主题和通知 ID 应支持接收端/邮件内容幂等识别。

#### H4.5 应用生命周期

调整 `internal/app/app.go`：

- 启动时和每个生命周期 tick 处理有限数量的核心事件/通知/插件任务。
- 采用轮询或配额避免某一种失败任务饿死定时发布；每次 tick 总处理量有上限。
- 定时发布成功后仍可触发搜索同步，但搜索同步失败不能影响已提交的发布事件。
- 停机时取消 worker context，等待现有短任务在 shutdown timeout 内结束；未完成任务依赖租约恢复。

#### H4.6 测试

- 手动发布、定时发布均产生完全一致的事件名/版本/公开 ID 语义。
- 业务事务失败时，内容状态和事件任务同时回滚。
- 事务提交后模拟进程退出，重启后事件只产生一个幂等投递。
- running 任务租约过期后可再次领取；并发 worker 不能同时成功 claim 同一任务。
- 第 1 至第 5 次失败的 `available_at`、状态和错误均正确，第 5 次后进入 failed。
- 手动重试可恢复，且不会创建新业务结果。
- 禁用插件不会消费该插件已有任务，但不会删除任务或插件数据。
- 通知、Webhook、Newsletter 的网络失败都能被统一观察和重试。

### H5：最小运维可见性

本阶段不建设完整监控平台，只补齐使失败可操作的最小后台入口。

建议修改：

- `internal/operations/query.go`：扩展 `Status`，返回 pending/running/failed 任务数、最早失败时间、最近错误类别、备份新鲜度和迁移版本。
- `internal/operations/http.go`：增加受保护的运维摘要和任务列表/重试接口；所有 POST 均复用当前 CSRF/Origin/会话策略。
- `web/admin/templates/`：新增任务状态页，展示任务 kind、状态、尝试次数、下次执行时间、脱敏错误和重试按钮，不展示 payload。
- `internal/app/app.go`：把 Operations 查询接入管理台导航或 Dashboard 状态卡片。
- `docs/reference/events.md`、`docs/extensions.md`：补齐事件和任务的版本、幂等、重试和禁用语义。

安全要求：重试只能针对当前站主有权查看的任务；任务查询不接受任意 SQL/表名；错误详情固定长度并过滤 token、邮箱、Authorization 和 URL 查询秘密；每次人工重试写入审计。

## 7. 实施顺序与依赖

### 7.1 推荐纵向切片

| 切片 | 包含任务 | 依赖 | 切片出口 |
| --- | --- | --- | --- |
| S1 客户端身份 | H0、H1 | 无 | 三个调用模块使用同一 resolver，伪造头测试通过 |
| S2 公开写入 | H2.1、H2.2、H2.3、H2.4 | H1；迁移 00011 | 评论/Newsletter 并发重放和 token 生命周期通过 |
| S3 网络适配器 | H3 | H0 | Webhook/Newsletter/SMTP 的网络边界和错误分类通过 |
| S4 任务基础设施 | H4.1、H4.3、H4.4 | H0、S3 | 统一 claim/lease/retry，已有 Webhook 测试保持通过 |
| S5 发布事件 | H4.2、H4.5 | S4 | 手动/定时发布和评论审核走事务任务，不再同步派发 |
| S6 运维与验收 | H5、全量测试 | S2、S4、S5 | 失败任务可查看、重试，阶段出口证据完整 |

### 7.2 依赖图

```text
H0 ──┬──> H1 ──> H2 ───────────────┐
     ├──> H3 ──> H4.1 ──> H4.2 ────┼──> H5 ──> 阶段验收
     └──────────────> H4.3/H4.4 ───┘
```

### 7.3 每个切片的交付规则

每个切片必须同时提交：

- 代码和最小必要迁移；
- 单元/集成/失败路径测试；
- 相关设计或参考文档更新；
- `gofmt`、`git diff --check` 和固定构建标签验证；
- 一条可回滚或可恢复的运维说明。

不要把“先改完所有基础抽象、最后再接业务”作为实施顺序；S2 和 S5 都必须以完整的端到端垂直切片结束。

## 8. 数据库与迁移计划

计划基线时最大迁移编号为 00010；本阶段实际新增 00011 至 00013：

| 迁移 | 内容 | 注意事项 |
| --- | --- | --- |
| 00011 | 请求幂等记录、公开写入指纹、Newsletter token | 全部为新增表和索引；token/摘要不保存原文 |
| 00012 | 队列所需的附加索引/租约字段、Webhook 事件幂等字段、通知状态辅助字段 | 尽量采用新增字段；SQLite CHECK 变更需用完整重建并验证旧数据 |
| 00013 | Newsletter 历史邮箱摘要版本标记 | 启动时按 200 条一批从加密地址重算为密钥 HMAC，避免裸 SHA-256 留存或重复订阅 |

每个迁移必须验证：

- 空数据库从头执行；
- 迁移版本 10 的真实数据库向前升级；
- 迁移执行中断后重启；
- 备份后升级失败的恢复；
- 启动两次不会重复建表或重复索引；
- Down 只用于测试清理，不作为生产回滚工具。

清理规则：幂等记录、指纹和已消费 token 必须按 TTL 分批清理；不允许每次请求执行全表清理，也不允许让安全辅助表无限增长。

## 9. 验收测试矩阵

| 类别 | 场景 | 预期 |
| --- | --- | --- |
| Client IP | 无可信代理 + 伪造 XFF | 使用直接对端地址，伪造头不生效 |
| Client IP | 可信 Caddy + 多级 XFF | 得到第一个非可信地址，IPv4/IPv6 一致 |
| Client IP | 非法/超长/重复转发头 | 安全回退，不绕过限流 |
| Auth | 代理后不同客户端连续登录失败 | 限流按真实客户端和用户名生效 |
| Comments | 同一幂等键并发提交 | 一个业务结果，其余安全重放或 409 |
| Comments | 相同评论短窗口重复 | 不产生第二条评论 |
| Newsletter | 已存在/不存在邮箱订阅 | 外部响应不可枚举账号状态 |
| Newsletter | 确认/退订 token 过期、错用途、重放 | 不改变错误目标；成功操作幂等 |
| Network | Webhook/Newsletter 指向 loopback、私网、link-local | 初始化或执行时拒绝，不发请求 |
| Network | 公网 endpoint 302 到私网 | 不跟随重定向，任务按永久错误处理 |
| Network | DNS 多地址/解析变化 | 每次连接检查最终地址 |
| Events | 手动/定时发布 | 相同事件版本和公开字段，任务可追踪 |
| Events | 事务提交前失败 | 内容和事件任务同时回滚 |
| Events | 提交后进程退出 | 重启后继续，幂等键不重复派发 |
| Jobs | 租约过期/并发 claim | 只有一个执行者，过期后可恢复 |
| Jobs | 连续失败达到上限 | 进入 failed，不再忙循环 |
| Webhook | 同一事件重新派发 | delivery identity 稳定，目标可去重 |
| Mail | SMTP 临时失败 | 有限退避重试，最终失败可见 |
| Plugin | 插件禁用后已有任务 | 不执行、不删除，重新启用后按策略继续 |
| Privacy | 日志、审计、任务状态 | 无密码、token、完整邮箱、正文或原始 IP |
| Migration | 空库、版本 10 升级、重启恢复 | 迁移和数据一致性通过 |

## 10. 工程验证门禁

从仓库根目录执行，始终使用项目固定构建标签：

```bash
make test
make test-race
make vet
go mod verify
make perf-gate
docker compose config
git diff --check
```

身份、公开写入、外部网络、迁移和任务改动还必须执行：

```bash
make stage3-acceptance
BROWSER_STRICT=1 make browser
```

浏览器门禁需要真实启动 Compose 实例；如果本机缺少 Go 或 Playwright，使用仓库已有 Docker/脚本替代，但在交付记录中明确标注未运行项。新增网络适配器应在测试中使用可控 resolver/transport，不用不稳定的公网服务作为唯一证据。

## 11. 发布、回滚与故障处理

### 11.1 发布前

1. `blog backup create --reason pre_upgrade` 并执行 `blog backup verify`。
2. 确认迁移版本、工作队列、失败任务和数据锁状态。
3. 先部署只包含新增表/字段和兼容读取的版本，再启用新 worker 行为。
4. 使用隔离数据库验证手动发布、定时发布、评论审核、Newsletter token 和失败重试。
5. 通过真实 Caddy 链验证可信代理配置；未配置可信网段时应安全回退到代理对端地址。

### 11.2 回滚原则

- 迁移只向前，不使用 `blog migrate` 做生产降级；出现问题使用已验证备份恢复或发布兼容旧 schema 的二进制。
- 新增表和字段优先采用 expand/contract，避免旧二进制因为未知表而无法启动。
- durable event 记录/队列不可用时，发布事务应失败并提示，不得静默发布后丢事件。
- 临时关闭外部插件只能停止消费和外发，不能删除其任务、投递记录或订阅数据。
- 任务堆积时先暂停对应外部插件或调整有界并发，不能通过无限增加 worker、重试次数或内存上限掩盖问题。

### 11.3 故障恢复

- `running` 任务依靠 lease 在重启后恢复；人工重试保留原任务身份并写审计。
- Webhook/Newsletter/SMTP 的远程故障不回滚已成功的内容发布。
- 若目标端不支持幂等，交付记录必须明确标记 at-least-once 风险，不能对外宣称 exactly-once。
- 恢复后检查 `/livez`、`/readyz`、首页、已发布文章、任务状态和公开事件输出。

## 12. 文档同步清单

实施完成时同步更新：

- `docs/architecture.md`：客户端身份、统一任务队列和提交后事件流程；
- `docs/extensions.md`：Host 任务接口、事件幂等、禁用和重试语义；
- `docs/reference/events.md`：实际实现的事件矩阵、版本兼容和公开字段；
- `config.example.toml`、`.env.example`：可信代理和新增有界参数；
- `docs/security/`：转发头、公开写入、token、外发网络和隐私审计；
- `docs/progress/`：每个纵向切片的实现、验证、提交和剩余风险；
- `README.md`：部署在 Caddy/Nginx/平台反代后的可信网段配置和 Newsletter 确认流程。

## 13. 暂不纳入本阶段

以下内容保持在路线图后续阶段，不因为本阶段的队列或安全改造顺带扩大范围：

- 媒体封面端到端闭环；
- 主题设置、样例渲染和插件路由契约重构；
- 列表查询瘦身、taxonomy N+1 和搜索性能专项；
- 备份加密、异地备份和完整运维中心；
- Owner/站点设置、修订 diff、批量编辑和排程日历；
- GraphQL、公开写 API、Redis、独立消息队列、独立搜索服务和多用户模型。

这些项目可以复用本阶段的队列、客户端身份和网络边界，但不作为本阶段完成条件。

## 14. 阶段完成记录模板

实施每个切片后，在 `docs/progress/` 增加记录或更新对应记录，至少填写：

```text
切片：S1 / S2 / S3 / S4 / S5 / S6
实现范围：
数据库迁移：
新增/修改事件与任务：
失败路径与重启验证：
安全/隐私检查：
自动化测试：
真实 Compose/Caddy 验证：
浏览器/性能验证：
Git 提交：
剩余风险：
```

只有实现、失败路径、重启、隔离和文档证据同时存在，才可以将对应切片从“进行中”改为“完成”。
