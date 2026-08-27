# 阶段三实施记录

阶段三把外部迁移、只读集成和发行加固补到阶段二的可运行单体中，仍保持唯一站主、SQLite、低常驻内存和可选能力默认关闭。

## 已实现矩阵

| 范围 | 实现 | 验收证据 |
|---|---|---|
| WordPress WXR / Ghost JSON / Markdown 导入 | `internal/importer`、`blog import` | 离线解析、HTML/Mobiledoc 保守转换、ZIP/目录限额、指纹去重、dry-run JSON 报告、只创建草稿 |
| 只读内容 API | `internal/contentapi`，`/api/v1/site`、`posts`、`pages` 及单项 | 插件关闭为 404；启用后只读公开投影、可选 Bearer、ETag/304；无 POST/PUT/DELETE |
| 签名 Webhook | `internal/webhooks`、`webhook_deliveries` | 发布/评论审核提交后入队，HMAC-SHA256，短租约、最多五次退避重试、失败可见 |
| 主题/插件开发者契约 | `docs/development`、`docs/reference`、`themes/example` | API 版本、视图模型、插槽、事件版本和示例主题文档化 |
| 跨平台发行物 | `Dockerfile`、`scripts/release.sh`、SBOM/许可证脚本 | buildx `linux/amd64,linux/arm64` OCI 输出、SHA256SUMS、SBOM provenance |
| 安全/恢复/性能/浏览器 | `docs/security`、`internal/perf`、`tests/browser` | race/vet/module 校验、ADR-0031 自动门、三浏览器 Playwright 夹具、隔离备份恢复脚本 |

## 离线导入命令

```bash
blog import wordpress --input export.xml --dry-run --report report.json
blog import ghost --input ghost.json
blog import markdown --input content/ --dry-run
```

每次真实导入报告来源指纹、计划数、创建草稿数、冲突、跳过项和有损转换警告；重复来源只报告跳过，不覆盖既有内容。

## 真实环境验收清单

1. 固定 Compose 启动后检查 `/livez`、`/readyz`、首页和管理端保护。
2. 在隔离临时实例完成站主/TOTP 初始化，启用内容 API 与 Webhook，验证 API JSON、签名头和重试状态。
3. 用真实 WXR、Ghost 和 Markdown 样本执行 dry-run 与导入，确认草稿和重复指纹。
4. 从上一迁移版本升级到当前版本，执行备份校验、恢复演练和公开结果对比。
5. 执行 `make perf-gate`；浏览器环境可用时执行 `BROWSER_STRICT=1 make browser`。

本轮已在实际内置浏览器中完成搜索页回归：移动视口 474×863 与参考图尺寸 2024×948 均无横向溢出，关键词/分类/标签输入框、两个下拉框和提交按钮的计算高度分别稳定为 58px 与 84px；下拉框关闭 Safari 原生外观并保留可见 CSS 箭头。文章标题也收敛为移动约 62px、桌面约 96px，并用长中文标题检查换行与溢出。`tests/browser/core.spec.ts` 和隔离夹具 `tests/browser/article.spec.ts` 同步加入断言；Chromium、Firefox、WebKit 主站回归 9/9 通过，隔离文章回归 12/12 通过。

本轮还用新构建的 `personal-blog:dev` 在临时容器中完成了三种导入 dry-run、Markdown 草稿导入与重复指纹跳过；启用 Bearer 的 `/api/v1/site` 实际返回 200，缺少凭据返回 401，条件请求返回 304；备份创建、校验和恢复演练均通过（迁移版本 10）。Compose 应用容器按本机 arm64 原生运行，实测常驻约 23 MiB / 256 MiB，Caddy 约 13 MiB / 64 MiB。
