# 改进阶段一实施记录：生产安全与一致性

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

实施日期：2026-09-02
对应计划：[阶段一生产安全与一致性实施计划](../development/phase-one-production-hardening-plan.md)
状态：阶段完成，工程门禁全部通过（2026-09-02）

这里的“改进阶段一”不是 `docs/progress/stage-1.md` 记录的 1.0 核心闭环，而是路线图中的生产安全与一致性阶段。

## 交付范围

| 切片 | 已实现内容 | 主要证据 |
|---|---|---|
| S1 客户端身份 | `clientip` 统一解析器、可信代理 CIDR、Identity/Comments/Analytics/Newsletter 接入、Compose/Caddy 配置 | `internal/platform/clientip/*_test.go`、配置测试 |
| S2 公开写入 | keyed 幂等、短期指纹、评论重复提交保护、Newsletter 确认/退订 token、通用响应 | `internal/platform/publicwrite/*_test.go`、评论和 Newsletter 集成测试 |
| S3 网络适配器 | `netguard` URL/地址/DNS 连接边界；Webhook、Newsletter、SMTP 使用有界网络调用 | `internal/platform/netguard/*_test.go`、适配器测试 |
| S4 任务基础设施 | `operations.TaskQueue` 统一入队、短租约、退避、最大尝试次数、过期恢复、人工重试和审计 | `internal/operations/tasks*_test.go`、Webhook/通知测试 |
| S5 发布事件 | 手动/定时发布和评论审核在事务内登记事件 outbox 与任务，提交后异步派发 | `internal/publishing/event_test.go`、迁移测试 |
| S6 运维可见性 | CLI 状态扩展、`/admin/operations/tasks` 受保护任务页、脱敏错误、CSRF 重试 | `internal/operations/tasks_http_test.go` |

## 数据库迁移

- `00011_phase1_safety.sql`：请求幂等、公开写入指纹、Newsletter token、事件 outbox。
- `00012_phase1_queue.sql`：通知租约/幂等字段、Webhook 事件幂等字段、任务 claim 索引，并为旧 pending 通知补建核心任务。
- `00013_newsletter_hashes.sql`：标记历史 Newsletter 邮箱摘要版本，由应用按批次从加密地址重算密钥 HMAC。
- `database.Open` 的最新迁移版本为 13；测试覆盖空库、从版本 10 升级、重复打开和迁移后重启。
- 清理由有界的 `publicwrite.Guard.Cleanup` 批处理，生命周期低频调用，不在每个公开请求中扫表。

## 关键语义

1. 未配置可信代理时，伪造 `X-Forwarded-For` 不改变客户端身份；配置代理时只接受明确 CIDR 下的合法链。
2. 同一显式幂等键绑定请求指纹；无幂等键的相同短期指纹不会产生第二条评论或重复订阅意图。
3. 发布/审核事务提交前失败时，业务数据、审计、事件 outbox 和任务一起回滚；提交后进程退出时，jobs 租约可恢复。
4. 外部调用不进入业务写事务；失败按统一任务策略退避，达到上限后进入 `failed`，管理台可用原任务 ID 重试。
5. 插件禁用只停止消费和外发，不删除插件数据、投递记录或任务。

## 验收矩阵

| 验收项 | 结果 |
|---|---|
| IPv4/IPv6、mapped IPv6、非法/超长/重复转发头 | 已有自动化测试 |
| 幂等键冲突、短期指纹、token 生命周期 | 已有自动化测试 |
| 任务并发 claim、租约过期、五次失败、人工重试审计 | 已有自动化测试 |
| 手动发布/定时发布/评论审核事件事务边界 | 已有自动化测试 |
| 外发 URL 语法、loopback/私网连接阻断、重定向策略 | 已有自动化测试；生产网络仍需按部署环境复核 |
| 管理台任务列表不展示 payload、POST 需要 CSRF | 已有自动化测试 |
| Compose/Caddy 实例健康、浏览器三引擎、性能门 | 见下方最终命令记录 |

## 最终验证命令

交付时从仓库根目录执行并把实际结果写入下表。Go 缺失时使用项目固定的 Go 1.26 Docker fallback，始终带 `fts5 sqlite_omit_load_extension` 标签。

| 命令 | 结果 |
|---|---|
| `go test -tags 'fts5 sqlite_omit_load_extension' ./...` | 通过（固定 Go 1.26 Docker fallback） |
| `go test -race -tags 'fts5 sqlite_omit_load_extension' ./...` | 通过（固定 Go 1.26 Docker fallback） |
| `go vet -tags 'fts5 sqlite_omit_load_extension' ./...` | 通过（固定 Go 1.26 Docker fallback） |
| `go mod verify` | 通过：all modules verified |
| `make perf-gate` | 通过：ADR-0031 in-process performance gate |
| `docker compose config` | 通过（实际执行 `docker compose config -q`） |
| `make stage3-acceptance` | 通过：测试、race、vet、模块校验、健康/首页检查 |
| `BROWSER_STRICT=1 make browser` | 通过：27 个用例，24 passed、3 skipped；Chromium/Firefox/WebKit 均通过 |
| `git diff --check` | 通过 |

本机未安装 Go，直接执行 `make test` 会因 `go: command not found` 退出；随后使用仓库约定的固定 Go 1.26 Docker fallback 完成了同一测试命令。`gofmt -l` 对仓库全部 Go 文件无输出。

## 真实运行态

- `docker compose up -d` 后 `app` 与 `caddy` 均正常运行，`app` 为 healthy。
- `blog status`：迁移版本 13；`pending_jobs`、`running_jobs`、`failed_jobs` 均为 0。
- Cel Panel 主题已安装并激活为 `cel-panel@2.2.4`；严格三浏览器回归使用该运行态主题通过。
- 未创建提交或推送远端；保留工作树交由用户复核和决定提交边界。

## 剩余风险

- 外部邮件/Webhook/provider 仍是 at-least-once；远端若在应用写回前已成功，重启可能重复投递，接收端需使用稳定 identity 去重。
- `BLOG_TRUSTED_PROXY_CIDRS` 是部署者责任；错误地扩大 CIDR 会重新扩大转发头信任边界。
- 备份加密、异地备份、媒体闭环、查询性能专项和完整运维中心不属于本阶段，按路线图后续阶段处理。
