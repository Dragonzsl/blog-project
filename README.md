# 个人博客系统

这是一个面向公开复用的单站点自托管博客系统。当前正在按纵向切片实现阶段一核心闭环，完成情况见[阶段一实施记录](./docs/progress/stage-1.md)。

核心方向：Go 模块化单体、SQLite、Markdown、服务端渲染、可上传主题、可信编译期插件，以及可在 1 核 1 GiB VPS 上稳定运行的硬性资源预算。

## 设计文档

- [领域词汇表](./CONTEXT.md)
- [技术选型](./docs/technical-selection.md)
- [总体架构](./docs/architecture.md)
- [数据模型](./docs/data-model.md)
- [主题与插件契约](./docs/extensions.md)
- [功能范围与交付路线](./docs/product-and-roadmap.md)
- [主流博客系统对照](./docs/research/popular-systems.md)
- [架构决策索引](./docs/adr/README.md)

## 一句话架构

公开请求由 Caddy 终止 TLS 后进入一个 Go 应用；Go 应用同时承载公开页面、管理后台、任务与 CLI 模式，使用同进程 SQLite 和本地持久卷，S3、邮件及外部集成均为可选适配器。

## 运行当前版本

当前切片提供应用骨架、SQLite 迁移及存活/就绪检查。需要 Docker Desktop 或 Docker Engine + Compose：

```bash
cp .env.example .env
docker compose up --build -d
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

本地 `localhost` 使用 Caddy 内部证书，因此命令行演示带 `--insecure`；部署到已解析的公开域名时，将 `BLOG_SITE_ADDRESS` 改为实际 HTTPS 地址，Caddy 会自动申请证书。运行数据保存在 `blog_data` 命名卷中，普通停止不会删除数据：

```bash
docker compose down
```

开发机已安装 Go 1.26 和 C 编译器时，可运行：

```bash
make test
make build
./bin/blog serve --config config.example.toml
```

发行构建必须使用 `fts5 sqlite_omit_load_extension` 标签；Makefile 和 Dockerfile 已固定这些标签。应用容器以非 root、只读根文件系统运行，默认限制为 0.85 CPU、768 MiB，Go 堆软上限为 640 MiB。

## 当前约束

- 每次部署只有一个站点和一个站主，不做角色、多租户或读者账户。
- 文章与页面正文以数据库中的 Markdown 为权威版本。
- 公开页面优先走可缓存快照，动态能力走独立接口。
- 主题可上传但不能执行服务端代码；插件随程序编译并按配置启停。
- 所有 1.0 功能按三个可运行的纵向阶段交付。
