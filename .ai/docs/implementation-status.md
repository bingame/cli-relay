# 实现与验收记录

日期：2026-09-17。依据 `relay-spec.md`，使用 Go / Cobra / modernc SQLite / go-keyring。文档使用中文；未修改 cc-switch、Multica 仓库，也未自动切换用户的真实原生配置。

## 阶段

| 阶段 | 交付 |
| --- | --- |
| 前置验证 | Codex 0.154.0 / Claude Code 2.1.268 参数与本地假服务验证，真实 cc-switch schema/导出头验证；各模块 NOTES |
| P0 | Provider SQLite/AES-GCM、两个 Adapter、mock 契约、add/list/remove/switch/run/status、私有文件权限 |
| P1 | SQL 内存导入与快照加密、冲突重命名、dry-run；受监管执行、退出分类、会话记录、子进程树收尾 |
| P2 | relay-handoff Skill、Markdown/YAML/JSON Schema 验证、活 agent 输出接收、continue 与硬约束重复强调 |
| P3 | Codex 官方 thread/read 优先、无头 resume、容错 JSONL、可选总结供应商、来源凭据脱敏、降级标识 |
| P4 | 仅独立接口预留；不含重试循环、通知发送或关机 |
| P5 | render-args / render-env JSON 对接；二进制别名包装；Codex 的 Multica 风格双向 stdio 完整本地验证 |

## 已运行检查

- Windows：`go test ./...`、`go vet ./...`。
- 真实 SQL 完整 CLI 验证：18 条供应商、15 条支持的 target 渲染，61 条加密秘密数据、1 份加密 MCP/prompts 快照；dry-run 无写入，导入不自动 switch，输出及落盘文件未发现已识别凭据明文。
- 适配器真实 Codex + 127.0.0.1 假 SSE：override、独立 profile、switch 后默认配置三种路径。
- `scripts/verify_codex_relay.py`：真实 Relay CLI 的 exec 退出 0，鉴权/模型正确，会话记录成功；二进制别名完成 initialize、thread/start、turn/start、thread/read，及时转发 JSONL，退出 0。
- WSL Ubuntu 20.04：实际执行 Linux process 和 cli 测试，包括 `syscall.Exec` 后 PID 不变、原生退出码保持、取消清理、超长行、脱敏、供应商切换隔离、dry-run、Handoff 降级及硬约束注入。
- CGO=0：Windows amd64、Linux amd64、macOS arm64 构建；macOS 未在真机运行。
- Skill：`python -X utf8 <skill-creator>/scripts/quick_validate.py skills/relay-handoff` 通过。
- 未运行真实付费供应商请求、未创建 Multica 服务端 agent。Windows 环境无 GCC，本轮未执行 race detector。

## 相对初稿的必要修正

1. SQL 校验使用常量的真实值 `-- CC Switch SQLite 导出`；按列名读取，不依赖列序号。
2. Codex 新版 profile 为独立文件；默认用完整 `-c` 覆盖，临时启动不需要修改全局配置。
3. Codex shell 环境策略过滤 shell 子进程，不控制自身 env_key 读取；未扩大 allowlist。
4. Claude 凭据仅经进程环境注入，不写 settings 文件。为避免原生 settings 覆盖凭据，隔离 settings 来源；实测这也禁用 `CLAUDE.md` 自动加载，须显式传入。Multica 若强制注入自己的 Claude `--settings`，当前组合明确拒绝，见 README。
5. Multica `--custom-env-file/stdin` 实际读取 JSON，所以提供 `render-env --format json`；原默认 dotenv 保留。
6. 可执行路径不能是带参数的命令字符串。复制/链接同一 Relay 二进制为 relay-codex/relay-claude 时启用包装入口。
7. 活 agent 内存不能由外部 Relay 直接读取；`--live` 输出请求，Skill 生成真实文档后通过 `--input` 接收。

## 运行边界

会话语义摘要仍由模型生成，结构验证不能证明事实或硬约束完全无遗漏。未知内部日志格式可能无法提取；无可用模型时降级会明确报错。多 target switch 逐项提交；没有跨文件全有或全无事务。密钥库尚无跨机器迁移命令。

本次输出位于 `bin/`（已忽略），主要入口是 `bin/relay.exe`。详细操作见项目根 README。
