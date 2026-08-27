# 总体架构

## 架构形态

系统是一个模块化单体。公开页面、管理后台、内部接口、任务执行器和 CLI 共用同一领域与基础设施代码，并默认编译成一个应用程序；Caddy、对象存储和邮件服务位于应用边界之外。

```text
Browser / Feed reader
        |
   HTTPS + compression
        |
      Caddy                 optional existing reverse proxy
        |
   Go application
   |      |       |
 Public  Admin   CLI/Jobs
   |      |       |
   +-- application modules --+
              |
          SQLite WAL -------- local media/cache/backup volume
              |
          optional adapters -- S3 / SMTP / Webhook targets
```

不存在内部 HTTP 微服务调用。模块间使用 Go 接口、应用服务和提交后的领域事件；持久任务写入同一个 SQLite 数据库。

## 模块边界

模块数量刻意控制在八组。每组可以包含多个包，但不能因为一个表或一个页面就创建新“服务”。

| 模块 | 拥有内容 | 对外能力 |
|---|---|---|
| Identity | 站主、会话、TOTP、恢复码 | 初始化、登录、恢复、授权判断 |
| Publishing | 文章、页面、编辑快照、版本、发布状态 | 编辑、预览、发布、定时、撤回、回收站 |
| Organization | 分类、标签、导航、永久链接、重定向 | 内容组织与链接解析 |
| Media | 媒体、变体、引用关系、存储位置 | 上传、处理、迁移、引用与安全删除 |
| Presentation | 主题、视图模型、Markdown 渲染、页面缓存 | 公开页面和主题预览 |
| Discovery | 搜索索引、SEO、RSS、Sitemap | 搜索与机器可读发布输出 |
| Operations | 配置、任务、备份、迁移、审计、健康 | 启动、升级、恢复和诊断 |
| Extensions | 插件注册、配置、钩子和官方插件 | 评论、统计、内容 API、Webhook 等可选能力 |

Identity 不提供通用用户/角色抽象；第一版只有唯一站主。Publishing 不知道 HTML 模板细节，Presentation 不直接更新文章表。插件只能调用显式暴露的宿主能力，不能拿到裸数据库句柄。

## 代码目录建议

```text
cmd/blog/                    程序入口和 serve/backup/restore/import 等命令
internal/
  platform/                  配置、数据库、事务、日志、时钟、ID、HTTP 公共件
  identity/
  publishing/
  organization/
  media/
  presentation/
  discovery/
  operations/
  extensions/
  contentapi/                 可选只读 `/api/v1` 插件
  webhooks/                   可选签名投递与持久重试
  importer/                   离线 WXR/Ghost/Markdown 解析器
plugins/                     官方编译期插件，每个插件一个目录
  comments/
  analytics/
  contentapi/
  webhooks/
web/admin/                   管理后台模板、CSS、TS 交互组件
themes/default/              默认现代编辑式主题
db/migrations/               顺序编号、只向前的 SQL 迁移
docs/
```

每个模块内部优先使用 `model.go`、`service.go`、`repository.go`、`http.go` 这类少量文件；只有文件明显过深时再拆包。不得建立 `controllers/services/repositories` 这种跨全项目水平分层目录。

## 依赖规则

```text
HTTP / CLI adapter -> application service -> module repository port
                                      -> after-commit event collector
repository adapter -> SQLite
theme renderer     -> immutable public view model
official plugin    -> narrow Host capabilities
```

- HTTP handler 不直接写 SQL。
- 模块不得查询其他模块私有表；跨模块读通过应用查询接口或专用只读投影。
- 事务由调用方应用服务控制，同一业务动作只创建一个 SQLite 写事务。
- 领域事件在事务提交后派发；需要重试的外部副作用先写入持久任务表。
- 不建立“万能 AppContext”或全局 service locator。

## 发布写入流程

1. 站主提交 Markdown 与结构化元数据。
2. Publishing 校验 slug、状态转换、时间与编辑快照版本。
3. 一个事务内保存内容、创建不可变版本、更新媒体引用，并登记提交后事件。
4. 提交成功后立即提高站点 `render_epoch`，使旧公开缓存不可命中。
5. 持久任务依次更新 FTS5、预热关键页面、刷新 RSS/Sitemap，并投递 Webhook。
6. 任一异步任务失败只记录重试，不回滚已完成的发布；后台明确显示“已发布但派生任务失败”。

定时发布不是内存定时器。任务执行器按数据库中的 `scheduled_at` 查询到期内容，并通过带条件的事务确保同一内容只发布一次；程序重启后自然继续。

## 公开读取流程

1. 路由解析永久链接和重定向。
2. 使用 `站点 + 路径 + 查询语义 + 主题版本 + render_epoch + 编码` 形成缓存键。
3. 命中时直接返回完整响应、ETag 和缓存头。
4. 未命中时从只读查询获取已发布视图，渲染 Markdown，应用允许列表清洗，再交给主题模板。
5. 生成完整响应后原子写入磁盘缓存，并把小型热点条目放入有界内存缓存。

缓存只保存公开且无身份差异的 GET/HEAD 响应。预览、搜索查询、后台、登录、评论提交和错误响应不进入页面快照缓存。阅读量和评论数量通过小型独立接口加载，不能污染主体页面缓存。

## 缓存失效

第一版不维护逐页面依赖图。任何会改变公开呈现的操作提高单调递增的 `render_epoch`；新请求使用新命名空间，旧文件由后台任务分批清理。这个策略会使首次访问重新渲染，但能消除漏失效和复杂依赖图。

静态主题资源使用内容哈希和一年 immutable 缓存；媒体 URL 包含稳定媒体 ID 与变体版本，替换原图时产生新版本而不是原地覆盖缓存内容。

## 任务执行器

任务表使用 `pending/running/succeeded/failed` 状态、可见时间、尝试次数、短租约、有限指数退避和幂等键。单进程默认一个串行工作循环；图片处理、备份和 Webhook 分别有独立并发上限，但总并发受 1 GiB 预算约束。插件任务最多执行五次，过期租约会在重启后重新变为可执行，最终失败可在状态页审计。

任务必须幂等。进程崩溃后，过期租约回到可执行状态。失败使用有上限指数退避并进入后台可见的失败列表；不引入 Redis、Kafka 或外部队列。

## 配置边界

配置分三层：

1. 启动配置：监听地址、数据目录、可信代理、日志、内存上限、数据库参数；来自 TOML 与环境变量，修改后重启。
2. 站点配置：名称、主要语言、时区、导航、SEO、RSS；存入 SQLite，修改后提高渲染版本。
3. 扩展配置：主题和插件各自拥有带版本的 schema；由宿主校验并存储。

秘密值不写入普通站点配置或导出归档。配置只保存 secret reference，具体值来自环境变量、Docker secret 或权限受限文件。

## 部署拓扑

默认 Compose 只需 `app` 和 `caddy` 两个容器，以及 `data`、`caddy_data` 持久卷。SQLite、媒体、缓存和本地备份位于 `data` 下的独立子目录，便于权限与恢复检查。已有 Traefik、Nginx 或平台负载均衡器的用户可只运行 `app`。

应用以非 root 用户运行，根文件系统只读，只给数据目录写权限。健康检查分为进程存活和依赖就绪；部署脚本等待就绪后再切换流量。应用只信任显式配置的代理网段所提供的转发头。

## 安全基线

- 管理后台与公开写接口均使用同站会话 cookie、CSRF 防护和 Origin 校验。
- Cookie 设置 Secure、HttpOnly、SameSite；登录后轮换会话 ID。
- 登录、TOTP、恢复码、评论和预览链接分别限速。
- Markdown 原始 HTML 默认关闭，文章和评论使用不同清洗策略。
- 主题解包防止路径穿越、符号链接和压缩炸弹，并限制文件数与总大小。
- 内容安全策略默认禁止内联脚本；主题脚本使用打包资源，外部集成需插件声明并调整策略。
- 媒体以检测到的 MIME 与解码结果为准，不信任扩展名；SVG 默认拒绝。
- SQLite 关闭任意扩展加载；插件拿不到 SQL 和文件系统通用权限。
- 备份、恢复、升级和导入都先做完整性检查，并产生审计记录。

## 性能与资源分配

应用启动时设置与容器限制一致但保留 10% 左右余量的 `GOMEMLIMIT`。页面缓存、Markdown AST、模板集合和数据库连接池全部有界；不得依赖“内存够用时无限增长”。

建议的初始预算不是承诺值，必须由基准校准：应用常驻 100–220 MiB、Caddy 30–60 MiB、操作系统与文件缓存保留其余空间。图片处理和 Argon2id 是短时峰值，不能同时无界并发。最终是否达标只以 ADR-0031 的实测发布门槛判断。

## 故障语义

- SQLite 不可写：就绪检查失败，公开缓存仍可在只读降级模式提供已生成页面，但后台拒绝修改。
- 本地媒体不可用：页面仍渲染，媒体返回明确错误且状态页告警。
- S3/邮件/Webhook 不可用：相关任务重试，不阻塞文章发布。
- 主题启用失败：保持当前主题，保存验证报告；绝不让不完整主题接管站点。
- 搜索索引损坏：从已发布内容重建，数据库正文不受影响。
- 缓存损坏：删除并按需重建，缓存永远不是权威数据。
