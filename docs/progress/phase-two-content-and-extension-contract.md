# 改进阶段二实施记录：内容与扩展契约闭合

> 历史记录：版本、测试与待办仅代表记录时的状态。当前说明见[文档入口](../README.md)。

实施日期：2026-09-04
对应计划：[阶段二内容与扩展契约实施计划](../development/phase-two-content-and-extension-contract-plan.md)
对应 ADR：[ADR-0038](../adr/0038-phase-two-content-and-extension-contracts.md)
状态：核心实现与本地服务实际验收通过；浏览器三引擎和 Docker Compose 运行态待环境补验

这里的“改进阶段二”是路线图中的“内容与扩展契约闭合”，不是 `docs/progress/stage-2.md` 的历史阶段记录。

## 交付范围

| 切片 | 已实现内容 | 主要证据 |
|---|---|---|
| S1 内容与封面 | 封面进入编辑快照、修订、发布/恢复/定时发布、媒体引用保护、公开媒体 view、主题卡片、RSS、归档和离线导入 | `internal/media`、`internal/publishing`、`internal/archive`、`internal/importer` 测试 |
| S2 主题契约 | settingsVersion/schema、设置校验与脱敏、媒体设置解析、确定性目录 checksum、六类 fixture、激活/回退/marker 重建 | `internal/presentation/theme_test.go`、主题包测试 |
| S3 插件契约 | 公开扩展槽、插件命名空间、注册失败隔离、设置迁移与敏感值处理、事件/任务版本和大小/幂等边界 | `internal/extensions/host_test.go` |
| S4 收口 | 迁移版本 15、ADR、参考文档、阶段一 SQLite 清理语法修复和运行态记录 | `00014`、`00015`、ADR-0038、启动日志 |

## 工程门禁

- `make build`：通过，固定 `fts5 sqlite_omit_load_extension` 标签。
- `make test`、`make test-race`、`make vet`：全量通过。
- `go mod verify`、`git diff --check`、`make perf-gate`：通过。
- `make stage3-acceptance`：脚本退出 0，Go/race/vet/module 通过；Docker daemon 无权限，Compose 运行态分支跳过，未将其宣称为通过。
- `make browser`：未完成。npm registry 探测无响应；`npm exec --offline -- @playwright/test --version` 报 `ENOTCACHED`，本机没有 Chromium/Firefox 可执行文件。

## 本地服务实际验收

- 最终 `bin/blog` 已启动于 `:8080`，迁移版本 15。
- `blog healthcheck`、`blog status` 通过；pending/running/failed jobs 均为 0。
- `/livez`、`/readyz`、首页、搜索、RSS、Sitemap、robots、登录页均返回 200；未登录管理入口返回 303；非法媒体路径返回 404。
- 示例配置默认关闭的内容 API 返回 404；首页未引入默认外部字体、CDN 或分析资源；启动日志无 panic、SQLite `near LIMIT` 或错误。

## Git 与剩余风险

- 本轮未执行新的提交或推送；阶段一已有提交保持不变。
- 用户既有 `themes/cel-panel/`、主题测试和 `.playwright-results/` 保留在工作树中，未混入其他模块提交边界。
- 具备 Playwright 三浏览器和 Docker daemon 权限后，应补跑 `BROWSER_STRICT=1 make browser` 及 Compose app/caddy 健康检查。
