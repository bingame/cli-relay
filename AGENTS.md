# 项目约定

- 文档和 Commit 使用中文。
- 遵循 `.ai/docs/relay-spec.md`；已验证的上游差异记录到各模块 `NOTES.md`。
- 密钥仅在加密存储、进程内存和明确请求的 `render-env` 输出中出现，不写 argv、配置文件或日志。
- 测试使用虚构凭据和隔离目录，不修改真实 Claude Code/Codex/Multica 配置。
