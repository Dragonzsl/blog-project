# 贡献指南

小修复和文档改进可直接提交 Pull Request。涉及产品范围、数据库、主题或插件契约的改动，先在 [Issues](https://github.com/Dragonzsl/blog-project/issues) 说明问题和方案。

## 开发环境

需要 Go 1.26、C 编译器和 Git。Docker Compose 用于部署测试；Node.js 和 Playwright 只在浏览器回归时需要。

```bash
make build
make run
```

本地站点为 `http://localhost:8080`，首次使用访问 `/admin/setup`。配置示例见 [`config.example.toml`](config.example.toml)。

## 修改与验证

先阅读 [AGENTS.md](AGENTS.md) 和相关[架构文档](docs/architecture.md)。一次 PR 只处理一个问题，沿用现有模块边界和服务接口。

Go 改动至少运行：

```bash
gofmt -w <修改的 Go 文件>
make test
make vet
git diff --check
```

直接运行 Go 命令时，加上 `-tags 'fts5 sqlite_omit_load_extension'`。没有本地 Go 环境时可使用：

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm   go test -tags 'fts5 sqlite_omit_load_extension' ./...
```

按改动类型补充验证：

| 改动 | 检查 |
|---|---|
| 认证、上传、导入、备份恢复、外部请求或迁移 | 失败路径、重启/幂等测试，`make test-race` |
| 模板、CSS、JS、主题 | 渲染测试、`make browser`；性能相关改动运行 `make perf-gate` |
| 发布或恢复 | `make stage3-acceptance`，隔离实例恢复检查 |
| 依赖或发行 | `go mod verify`、race、vet、性能门、SBOM 和许可证审查 |

浏览器默认访问 `https://localhost`；用 `BASE_URL` 指定测试站点。安装浏览器后使用 `BROWSER_STRICT=1 make browser`。部分场景还需要单独准备夹具，具体环境变量见 [`tests/browser`](tests/browser)。

## 提交 PR

说明解决的问题、行为变化和实际运行的检查。检查未执行或被跳过时写明原因。配置、命令、数据结构或扩展契约变化，应同时更新对应文档；已应用的迁移不能修改，要新增编号。

不要提交 `.env`、认证密钥、数据库、备份、导入报告、构建产物和测试截图。使用样例数据复现问题，日志须脱敏。

新增依赖需固定版本、说明用途和许可证，并评估内存与失败行为。项目使用 [Apache-2.0](LICENSE)。
