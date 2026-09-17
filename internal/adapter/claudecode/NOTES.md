# Claude Code 适配验证与实现边界

验证日期：2026-09-17。只读查看本机 `claude --version`、`claude --help`，版本为 **2.1.268**；参考本地 Multica 的 `server/pkg/agent/claude.go`。请求行为使用临时 `CLAUDE_CONFIG_DIR`、临时工作目录、虚构凭据和 `127.0.0.1` HTTP 服务验证，没有调用真实模型或修改用户配置。

## 启动与凭据

- 原生支持 `--settings <file-or-json>`、`--resume <id>`。无头模式使用 `-p --verbose --output-format stream-json`；保留调用方 prompt 与其余参数，相同协议参数归一化，冲突输出格式报错。
- 规范 §5.1 的明文 settings 与 §2、§10 及项目安全约定矛盾。实现以安全要求为准：JSON 渲染文件和全局 settings **不写供应商密钥**；`api_key` 通过子进程 `ANTHROPIC_AUTH_TOKEN` 注入，`env:NAME` 加密凭据注入对应环境变量。`render-env` 是唯一允许明确输出这些值的 CLI 接口。
- `switch` 只同步配置与当前供应商指针；原生 `claude` 进程不会自动读取 Relay 的加密密钥。使用 `relay run`、`relay exec` 或显式消费 `render-env` 来获得凭据。
- 已验证：即使进程环境已含 `ANTHROPIC_AUTH_TOKEN=fake-injected-token`，user settings 中的 `env.ANTHROPIC_AUTH_TOKEN=fake-global-token` 仍覆盖它，请求使用后者。仅清除父进程旧变量不足以隔离供应商。
- 因此受控启动固定使用 `--setting-sources "" --settings <产物>`。本地对照请求确认此时使用注入的虚构凭据，并忽略 user/project/local settings 的冲突凭据。调用方不得通过原生 `--settings`、`--setting-sources` 覆盖此隔离；需要保留的无密钥设置放进 `extra.claude_settings`，由导入器保留或手工配置。
- **同一对照验证确认：空 `--setting-sources` 同时关闭项目 `CLAUDE.md` 自动加载。** Relay 不声称保留该原生自动发现行为。需要项目指令时，显式提供 `--append-system-prompt-file <CLAUDE.md 路径>` 或在 prompt 中提供上下文；此处没有自行仿写 Claude 的项目指令搜索规则。管理员 managed settings 属于 Claude 自身策略，Relay 不绕过它。
- 在合并环境前删除旧身份、base URL、模型别名与 Bedrock/Vertex/Foundry 选择变量，再设置当前供应商环境。禁止同时注入 AUTH_TOKEN 和 API_KEY，避免认证方式歧义。

## 全局 JSON 管理

`ApplyGlobal(artifact, nativeHome)` 的 `nativeHome` 是 `.claude` 目录。JSON 不支持注释，使用同目录 `.relay-managed.json` 记录 Relay 管理的 JSON Pointer 叶子路径及最后写入值，并以 `.relay-settings.lock` 串行化切换。

切换时，仅删除与上次写入值仍完全相同的旧字段，随后合并新产物；用户添加的同级字段、以及不再被当前供应商管理的手工修改均保留。首次遇到不同的既有值、或用户修改了新供应商也需要管理的字段时，明确报错且不写配置。用户可以自行调整冲突字段后重试，程序不静默覆盖。数组视为完整叶子字段，不尝试逐项猜测来源。

配置和管理记录均按受保护文件写入；普通写入失败时恢复配置。两个独立文件无法提供数据库式的跨文件掉电原子性：若在写入两文件之间系统崩溃，后续切换可能因管理记录不匹配而保护性报错，需人工核对文件。不会以不匹配记录强制覆盖新值。

## 验证

`go test ./internal/adapter/claudecode` 覆盖无密钥产物、环境注入与清理、双重身份拒绝、原生协议归一化、路径校验、配置合并、过期管理字段清理、幂等切换以及手工编辑冲突保护。测试仅使用虚构凭据和 `t.TempDir()`。
