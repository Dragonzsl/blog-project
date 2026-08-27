# 阶段三安全、依赖与恢复审计

## 已审查边界

- WordPress/Ghost/Markdown 只离线读取；输入、ZIP 条目、解压总量和单文件均有上限，路径拒绝绝对路径、反斜杠、`..` 和符号链接。
- 导入只调用发布服务创建草稿，来源 SHA-256 指纹写入 `import_runs`，重复执行不会覆盖既有内容。
- 内容 API 仅注册 GET 路由，查询只走已发布投影；可选 Bearer token、ETag 和错误响应 `no-store`。
- Webhook 端点拒绝凭据和重定向，正文使用 HMAC-SHA256；网络调用在提交后持久任务中执行，最多五次退避重试，交付状态可审计。
- 主题包解包、模板解析和原子切换沿用阶段二安全限制，内嵌默认主题可回退。

## 发布前命令

```bash
go test -tags 'fts5 sqlite_omit_load_extension' ./...
go test -race -tags 'fts5 sqlite_omit_load_extension' ./...
go vet -tags 'fts5 sqlite_omit_load_extension' ./...
go mod verify
make perf-gate
make sbom
make license-audit
```

有 `govulncheck`、`go-licenses`、`syft` 的发布机应额外运行对应命令；脚本会记录工具是否缺失，不把部分结果伪装成完整扫描。

## 恢复演练

阶段三每次候选构建都要求在隔离数据目录执行 `blog backup create`、`blog backup verify`、`blog backup drill`，随后用 `blog restore --target-data-dir` 启动新实例并比较公开首页/API 输出。升级前先执行 `blog upgrade prepare`，升级失败只切回已验证的备份，不直接复制活动 SQLite/WAL 文件。

## 依赖许可

项目与官方主题/插件采用 Apache-2.0。直接依赖的许可证在发布报告中列出；接受 Apache-2.0、BSD、MIT、ISC、MPL-2.0，未知或强 copyleft 许可证必须在发行前阻断并由站主确认。
