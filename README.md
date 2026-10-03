# blog-project

单站点、单站主的自托管博客。使用 Go、SQLite 和 Markdown，公开页面与管理后台均由服务端渲染。生产环境无需 Node.js、Redis 或独立数据库服务。

当前版本为 [v0.1.0-alpha.1](https://github.com/Dragonzsl/blog-project/releases/tag/v0.1.0-alpha.1)，首次公开预览版。适合试用与反馈；升级兼容性尚未承诺，使用真实内容前请准备并验证备份。版本记录见 [CHANGELOG](CHANGELOG.md)。

## 功能

- 文章与页面：草稿、预览、定时发布、版本比较与恢复、批量操作、回收站。
- 分类、标签、导航、媒体库，中英文全文搜索。
- SEO、RSS、Sitemap、永久链接重定向和只读内容 API。
- 密码与强制 TOTP 登录、一次性恢复码、CLI 认证恢复。
- 一致性备份、校验、恢复演练、WordPress/Ghost/Markdown 导入和内容归档。
- 内嵌默认主题、可安装主题包；评论、统计、Newsletter、Webhook 等可选能力默认关闭。

一次部署只有一个站点和一个站主，不提供协作者、读者账户、付费会员或任意服务端插件上传。

## 轻量与性能

- **运行依赖少**：一个 Go 应用配合 SQLite，搜索使用内置 FTS5，后台任务在同一进程运行。默认部署只需应用和 Caddy，无需额外数据库、缓存或队列服务。
- **资源有上限**：默认 Compose 将应用限制为 0.85 CPU / 256 MiB，Go 内存软上限为 192 MiB；Caddy 另限 0.15 CPU / 64 MiB。连接池、页面缓存和任务批次均有界。
- **阅读负担小**：页面由服务端输出，JavaScript 用于渐进增强；默认主题不加载第三方字体、脚本或 CDN 资源。公开页面缓存复用渲染结果，内容变化时自动失效。
- **按需读取内容**：列表查询不加载正文，性能测试覆盖 10,000 条内容的分页查询，避免内容增长后每次请求扫描整站正文。

`make perf-gate` 检查默认主题 CSS 大小、Markdown 渲染、缓存读取、搜索和列表查询。Alpha 发布前已通过这些进程内测试；容器配额是资源限制，实际占用和访问容量取决于内容、插件与流量，测试结果不等同于公网压测。预算与测试范围见 [ADR-0031](docs/adr/0031-performance-budgets-are-release-gates.md) 和[性能测试](internal/perf)。

测试结果与复现入口：

- [性能优化后压测与验收](docs/progress/phase-three-performance-implementation.md)：10,000 篇文章的并发矩阵、目标 100 RPS 实测、冷渲染瓶颈与环境限制。
- [优化前压测基线](docs/progress/phase-three-concurrency-load-report.md)：历史错误边界和瓶颈定位，供前后对照。
- [万条内容规模测试](docs/progress/phase-three-scale-and-operations.md)：列表查询耗时、堆内存增量与门槛结果。
- [Content API 测试结果](docs/progress/phase-four-product-and-public-capabilities.md)：游标分页与增量查询；报告中的 `rps=444.9` 实际为内容条数/秒。
- [HTTP 压测代码](internal/app/phase3_http_load_test.go)：独立构建标签 `phase3load`，不随普通测试或 `make perf-gate` 运行。

上述报告是历史实测记录，测试环境和代码阶段见各报告；不代表当前版本在默认容器配额下的持续压测结果。

## 快速开始

需要 Docker Engine 或 Docker Desktop、Docker Compose v2 和 curl。默认占用宿主机的 80、443 端口。

```bash
git clone --branch v0.1.0-alpha.1 --depth 1 https://github.com/Dragonzsl/blog-project.git
cd blog-project
BLOG_VERSION=0.1.0-alpha.1 ./scripts/deploy.sh
```

首次运行会提示输入域名，留空使用 `localhost`。脚本生成权限为 `0600` 的 `.env`，构建镜像、启动服务并检查 HTTPS 入口；成功后输出站点和初始化地址。打开输出的 `/admin/setup`，创建站主、绑定 TOTP 验证器并保存恢复码。

使用 `localhost` 时为本地 Caddy 证书，浏览器可能提示不受信任。公网域名会自动申请和续期证书。

### 部署到服务器

先将域名的 A/AAAA 记录解析到服务器，开放 TCP 80、443 端口，再运行脚本并输入域名；也可以直接传入：

```bash
./scripts/deploy.sh --domain blog.example.com
```

Caddy 自动申请 HTTPS 证书。`BLOG_SITE_ADDRESS` 也用于生成 canonical、RSS 和 Sitemap；后台已保存公开基址时，还需在站点设置中同步修改。

重复运行脚本会重新构建和启动；已有运行中的应用会先创建升级前恢复点。已有 `.env` 会沿用；用 `--configure` 重新提示域名，自动化环境可用 `--domain` 或 `--non-interactive`。它只加载 `compose.yaml`，不自动加载本地覆盖文件。可选服务的配置方式见[使用手册](docs/usage.md)。

数据保存在 `blog_data` 命名卷的 `/data/site`。停止服务不会删除数据：

```bash
docker compose -f compose.yaml down
```

不要使用 `down -v`，除非确定要删除数据卷。备份与恢复步骤见[使用手册](docs/usage.md)。

## 本地开发

需要 Go 1.26 和 C 编译器：

```bash
make build
make test
./bin/blog serve --config config.example.toml
```

访问 `http://localhost:8080/admin/setup`。Go 构建与测试必须带 `fts5 sqlite_omit_load_extension` 标签，Makefile 已配置。

## 文档与贡献

- [使用与运维](docs/usage.md)：配置、后台、备份恢复、导入和故障排查。
- [架构](docs/architecture.md)与[实现说明](docs/architecture-implementation.md)。
- [主题开发](docs/development/themes.md)、[插件开发](docs/development/plugins.md)、[内容 API](docs/api/content-api-v1.md)。
- [文档索引](docs/README.md)与[贡献指南](CONTRIBUTING.md)。

问题和功能建议请提交到 [GitHub Issues](https://github.com/Dragonzsl/blog-project/issues)。报告问题时附上版本、部署方式、复现步骤和脱敏日志。

## 许可证

[Apache-2.0](LICENSE)。依赖的许可证报告可通过 `make license-audit` 生成。
