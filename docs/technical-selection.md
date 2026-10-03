# 技术栈

本页列出仓库实际使用的技术。依赖版本以 [`go.mod`](../go.mod) 和 [`go.sum`](../go.sum) 为准。

| 用途 | 实现 |
|---|---|
| 语言 | Go 1.26；SQLite 驱动需要 CGO 和 C 编译器 |
| HTTP | 标准库 `net/http`、Chi v5 |
| 数据库 | SQLite WAL，`mattn/go-sqlite3`；一个写连接，默认两个读连接 |
| SQL 与迁移 | 手写 SQL、Goose 嵌入式迁移 |
| Markdown | Goldmark，渲染后用 Bluemonday 清洗 |
| 页面 | Go `html/template`，模板和静态资源由 `embed.FS` 嵌入 |
| 后台编辑器 | Markdown 文本框、原生 JavaScript 工具栏和预览 |
| 样式与交互 | 原生 CSS 与 JavaScript，无前端打包步骤 |
| 密码与第二因素 | Argon2id、`pquerna/otp` TOTP |
| 图片 | Go JPEG/PNG 编码器、`golang.org/x/image/draw` |
| S3 | 自定义 HTTP/SigV4 适配器，不使用 AWS SDK |
| 配置与日志 | TOML、环境变量、标准库 `log/slog` |
| 部署 | Docker Compose、Caddy、distroless nonroot 应用镜像 |
| 测试 | Go test/race/vet、Go benchmark、Playwright 三浏览器回归 |

HTMX、Milkdown、Vite、sqlc、Lighthouse CI 和 vegeta 不在当前构建或测试流程中。运行环境无需 Node.js；浏览器测试使用 Node.js 和 Playwright。

## 构建约束

所有 Go 构建、测试和静态检查都使用 `fts5 sqlite_omit_load_extension` 标签，启用 FTS5 并禁用 SQLite 动态扩展加载。常用命令见[贡献指南](../CONTRIBUTING.md)。

Dockerfile 固定构建镜像和运行镜像的摘要。发行脚本默认生成 Linux amd64/arm64 OCI 归档；仓库没有自动发布跨平台 CLI 的 CI 工作流。

## 依赖变更

新增运行时依赖需说明用途，固定版本，检查许可证，并验证内存占用和失败行为。不要因可选功能增加核心站点必须依赖的外部进程。发行时生成 SBOM 和许可证报告，见 [`scripts/release.sh`](../scripts/release.sh)。
