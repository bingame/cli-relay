# 安全说明

## 报告安全问题

请不要在公开 Issue、Pull Request、日志或截图中粘贴 API key、访问令牌、口令、`providers.db`、`vault.json` 或 `render-env` 输出。发现凭据泄露时，应先立即在供应商侧撤销并轮换，再通过 GitHub 的私密漏洞报告渠道联系维护者；没有私密渠道时，请先私下联系维护者。

## 凭据边界

- Relay 默认把供应商凭据保存在本机加密存储中。
- 默认的 Claude Code `apiKeyHelper` 和 Codex `auth.command` 回调不会把明文写入配置文件或命令行参数。
- `relay secret get` 和 `relay provider render-env` 会按设计把凭据输出到标准输出，只应在受信任的本地进程之间使用。
- 测试和 CI 使用虚构凭据与隔离目录，不应替换为真实凭据。

公开仓库不会包含用户本机的供应商数据库或密钥库；提交前仍应检查 Git 历史、发行归档和 CI 日志。
