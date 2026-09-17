# Codex 参数验证记录

验证时间：2026-09-17；本机 `codex-cli 0.154.0`，Go 1.26.4，Windows amd64。

## 实测结果

- `--profile` 和 `-c model_provider=...` 都可以启动，但 **0.134.0 起 profile 改为 `$CODEX_HOME/<name>.config.toml` 独立文件**；不再读取 `[profiles.name]`。本项目默认用 `-c` 传入不含密钥的供应商定义和 model 指针，不改全局文件。可选 profile 模式仅在配置已通过 switch 安装后使用。
- 无头恢复语法为 `codex exec resume <SESSION_ID> <PROMPT>`；交互恢复是 `codex resume <SESSION_ID>`。
- `shell_environment_policy` 过滤的是模型调用 shell 时的环境，**不是 Codex 自身读取 `env_key` 的环境**。把凭据加入 shell allowlist 并非鉴权前提，还会让模型执行的 shell 获取凭据。
- 旧语法 `include_only = ["PATH", "HOME"]` 仍可解析；当前推荐 `filters = { "RELAY_*" = "exclude" }`，不可在同一层混用 filters 和旧 exclude/include_only。
- `scripts/probe_codex.py` 使用临时 CODEX_HOME、虚构密钥、127.0.0.1 SSE 假服务。三组实测均退出 0、捕获正确 Authorization、产生 turn.completed：`-c` + shell inherit=none/include_only、独立 profile 文件、filters 排除 RELAY_*。未请求真实服务。

## 规范修正

遵循 §2/§10 的安全要求，凭据只经子进程环境注入，默认不主动扩大 shell allowlist。不把旧 `[profiles.x]` 写入新版本 Codex。临时启动内联供应商定义以保证未 switch 的 provider 也可用。静态片段不包含 API key。

## 依据

- https://developers.openai.com/codex/config-advanced/ （profile 文件迁移、配置覆盖）
- https://developers.openai.com/codex/config-reference/ （env_key、shell_environment_policy）
- 本机 `codex --help`、`codex exec resume --help` 与上述隔离探针。

## 实现边界与验证

- 默认 `LaunchMode=override`：`-c` 同时注入完整 `model_providers.relay_*` 定义及 model 指针，不依赖先前 `switch`。环境变量名包含供应商 ID 的摘要，避免 `a-b`、`a_b` 和大小写差异导致冲突。
- `Extra.codex_config` 接受已解析的原生 TOML 对象，按原 `model_provider` 选择供应商定义；仅映射模型与供应商相关字段，不从导入配置传播执行审批、安全沙箱或 shell 环境策略。`Provider.BaseURL`、`Provider.Model` 优先。
- Provider 经 SQLite JSON 列往返后数字会变为 Go `float64`，直接编码会生成原生整数配置不接受的 `3.0`。适配器对重试、超时、上下文窗口和自动压缩阈值这些已知整数字段恢复整数类型，并拒绝负数、小数或失去精度的浮点值；单元测试和本机假 SSE 测试均覆盖 JSON 往返路径。
- 明文字段（如 `api_key`、`experimental_bearer_token`、`http_headers`）拒绝渲染；额外鉴权用 `env_http_headers` 环境变量名映射和加密存储中的 `env:NAME`。所有凭据仅加入启动环境，配置产物和 argv 不含凭据。
- `ApplyGlobal` 使用 TOML 语法树修改已有顶层模型指针，保留同行注释、无关设置和多行字符串；供应商区块由 `BEGIN/END RELAY CODEX` 注释管理。拒绝覆盖同名的手动供应商或独立 profile。文件按安全写入接口替换，并使用配置锁避免多个 Relay 进程同时修改。
- 额外实测发现：Codex 0.154.0 对旧顶层 `profile = "..."` **直接拒绝启动**，错误要求改用 `--profile` 和独立 profile 文件。因此 `ApplyGlobal` 定点移除旧顶层 `profile` 赋值，保留同行注释、原独立 profile 文件以及历史 `[profiles.*]` 表。后者在当前版本不被使用，但实测保留它们不妨碍全局默认启动。
- 可选 `LaunchMode=profile` 只读取已经安装、内容与产物相符的 `<relay_id>.config.toml`；未安装或用户修改后报错，不隐式执行全局切换。配置路径遵循显式 `NativeHome`、`CODEX_HOME`、`~/.codex` 的优先级。
- Multica 本机代码通过 `codex app-server --listen stdio://` 使用 JSON-RPC。该入口保持参数透明，不追加 `exec` 或 `--json`；普通无头启动和 resume 则补齐 `exec` / `--json`。
- `IsProtocolBridge(nativeArgs)` 为监管层提供双向 RPC 入口判定，避免把已由外层处理的人工输入请求永久标成单次执行失败。判定跳过原生全局参数的值，`--model app-server` 等参数值和 `--` 后的 prompt 不会被误识别。
- `go test ./internal/adapter/codex` 使用虚构凭据和临时目录，覆盖参数 TOML 可解析、静态文件无密钥、无全局配置时临时启动、环境变量冲突、全局合并幂等与多供应商共存、手动配置冲突、profile 显式安装、Multica app-server 透传及交互/无头恢复。

## 真实适配器集成验证

`integration_test.go` 的 `TestLocalCodexIntegration` 默认跳过，显式设置 `RELAY_CODEX_INTEGRATION=1` 后运行；可用 `RELAY_CODEX_BINARY` 指向本机原生二进制。Windows 默认会在 npm 的 `@openai` 安装目录寻找 `codex.exe`，不通过 shell 包装器调用。

```powershell
$env:RELAY_CODEX_INTEGRATION = '1'
go test ./internal/adapter/codex -run TestLocalCodexIntegration -v
```

2026-09-17 在 Codex 0.154.0 实测以下三组通过：

1. `Render` + `BuildLaunchInputs` 的真实内联 TOML 参数，无事先 switch。
2. `ApplyGlobal` 安装后，`LaunchMode=profile` 的独立 profile 参数。
3. 带旧顶层 profile 指针和历史 `[profiles.*]` 表的配置经 `ApplyGlobal` 处理后，直接执行 Codex 的全局默认配置，不附加供应商覆盖参数。

所有请求只指向测试内创建的 `127.0.0.1` HTTP SSE 服务；服务验证 Authorization 与模型名，Codex 输出 `turn.completed`。宿主环境采用白名单构造，HOME、USERPROFILE、APPDATA、LOCALAPPDATA、CODEX_HOME 和工作目录均为临时目录，不继承真实凭据，不修改真实配置。测试配置显式排除 shell 的 `RELAY_*` 环境变量，以上三组仍完成 provider 鉴权；适配器不放宽任何 shell allowlist。

## Multica 参数归一化核对

依据本地 `E:/GitHubNew/multica/server/pkg/agent/codex.go` 的 `NormalizeCodexLaunchArgs`、`filterCodexConfigOverrides` 和启动流程，以及 `claude.go` 的 `filterCustomArgs` / `unshellQuoteArg`：

- `NormalizeCodexLaunchArgs` 移除用户传入的 `--listen`，当任务配置托管 MCP 时移除 `-c mcp_servers.*=...`。Relay 的 `model_providers.*`、`model_provider`、`model` 和模型选项不属于这些命名空间，不被过滤。
- `unshellQuoteArg` 只移除完整 argv 项外围的 shell 引号或 flag 等号后的 shell 引号；`model='test'`、`model_providers.relay_*= {...}` 等普通赋值的 TOML 引号原样保留。Relay 必须以 JSON argv 数组提供参数，不能再包一层 shell 命令字符串。
- 启动流程另会在任务 CODEX_HOME 存在时删除用户对 `shell_environment_policy` 的覆盖，保留 Multica 自己的策略。Relay 默认参数不修改该命名空间；服务鉴权使用 Codex 主进程环境，不依赖模型 shell 获得密钥。
- 原生入口为 `app-server --listen stdio://`，之后追加上述归一化参数。Relay `NativeArgs` 对该入口保持透明。
