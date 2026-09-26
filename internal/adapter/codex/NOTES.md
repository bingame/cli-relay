# Codex 参数验证记录

## Skill 安装与发现（补充）

2026-09-17 获取 [OpenAI Agent Skills 官方文档](https://developers.openai.com/codex/skills/)：Codex 原生支持包含 name/description frontmatter 的 `SKILL.md`，文档当前推荐用户路径 `$HOME/.agents/skills`。不能再声称 Codex 没有原生 Skill。

本机 Codex 0.154.0 的 `app-server skills/list` 实测：临时 `$CODEX_HOME/skills/relay-probe-codex/SKILL.md` 被列为 `scope=user`、`enabled=true`。因此按产品指定路径安装到 `$CODEX_HOME/skills/relay-handoff`（未设置时 `~/.codex/skills`），不另外创建可能重复发现的 `.agents` 副本。探针只创建临时目录、初始化 RPC 并列 Skill，不请求模型、不修改真实配置；Windows 的 Codex 仍可能从系统用户目录只读扫描其他已有 skills，这不代表环境变量改变了所有原生扫描根。

验证时间：2026-09-17；本机 `codex-cli 0.154.0`，Go 1.26.4，Windows amd64。

## 实测结果

- `--profile` 和 `-c model_provider=...` 都可以启动，但 **0.134.0 起 profile 改为 `$CODEX_HOME/<name>.config.toml` 独立文件**；不再读取 `[profiles.name]`。2026-09-18 官方文档进一步确认，Codex 先加载主 `config.toml`，再 overlay 独立 profile。因此 Relay v0.5 在主配置注册 provider，profile 只写选择器，启动统一使用 `--profile`。
- 无头恢复语法为 `codex exec resume <SESSION_ID> <PROMPT>`；交互恢复是 `codex resume <SESSION_ID>`。
- `shell_environment_policy` 过滤的是模型调用 shell 时的环境，**不是 Codex 自身读取 `env_key` 的环境**。把凭据加入 shell allowlist 并非鉴权前提，还会让模型执行的 shell 获取凭据。
- 旧语法 `include_only = ["PATH", "HOME"]` 仍可解析；当前推荐 `filters = { "RELAY_*" = "exclude" }`，不可在同一层混用 filters 和旧 exclude/include_only。
- `scripts/probe_codex.py` 使用临时 CODEX_HOME、虚构密钥、127.0.0.1 SSE 假服务。三组实测均退出 0、捕获正确 Authorization、产生 turn.completed：`-c` + shell inherit=none/include_only、独立 profile 文件、filters 排除 RELAY_*。未请求真实服务。

## 模型目录（model_catalog_json）实测（2026-09-22）

`model_catalog_json` **不是"模型 ID 列表"，而是一份完整的模型定义目录**：把它当数组写会在启动时直接报错。

- 报错实证（codex-cli 0.155.1）：`failed to parse model_catalog_json path ...: missing field `slug` at line 7 column 5`。
- 每条目必需字段：`slug`、`display_name`、`supported_reasoning_levels`、`shell_type`、`visibility`、`supported_in_api`、`priority`、`support_verbosity`、`truncation_policy`、`experimental_supported_tools`，并且必须给出 `base_instructions` 或 `model_messages.instructions_template`（两者都缺时报 `missing field `base_instructions``）。
- **目录条目是该 slug 的替代，没有内置继承**：给出 `slug = "gpt-6-astra"` + 空 `base_instructions` 时，请求里的 instructions 就是空的；省略 `base_instructions` 则直接拒绝加载。所以自定义条目的提示词要么自备，要么显式引用 `model_messages`。
- 顶层形态为 `{"models": [ ... ]}`；`codex debug models --bundled` 在 0.155.1 上 34ms 导出 443829 字节、9 个条目（每条 instructions 12896–21261 字符），可作模板来源，但 Relay 不内置官方提示词副本。
- 设置了 `model_catalog_json` 后，Codex **不会**再去拉取 provider 的 `/v1/models`；不设置时，`auth.command` 模式会发 `GET /v1/models?client_version=0.155.1` 并回落到内置通用提示词（17174 字符，以 "You are a coding agent running in the Codex CLI," 开头），`env_key` 模式不拉取。
- 第三方 `/responses` 网关会拒绝 Codex 的 freeform `apply_patch`（`type=="custom"`）工具，因此 Relay 的目录条目不声明 `apply_patch_tool_type` / `web_search_tool_type` / `tools` / `model_messages`，改用 `shell_type = "shell_command"`。这与 cc-switch（`E:\GitHubNew\cc-switch`，`src-tauri/src/resources/codex_native_responses_template.json` 与 `codex_config.rs` 的 `codex_catalog_model_entry`）做法一致：cc-switch 为每个非官方模型克隆中性模板、覆写 slug/display_name/description/context_window/priority(1000+序号)，并把 `base_instructions` 保留为一句中性描述。
- Relay 的实现取同一形态：`catalog.go` 里内置一份中性模板（slug/display_name/description 为 `relay-template`，`base_instructions` 为 cc-switch 使用的同一句 117 字符中性提示词，`default_reasoning_level = high`，`supported_reasoning_levels` 为 none/high），逐模型克隆后覆写。差异：`context_window`/`max_context_window` 优先取 `provider_models.context_window`，其次取 `Extra.codex_config.model_context_window`，都没有时用 128000；条目里声明的 `model_reasoning_effort` 会按 `none < minimal < low < medium < high < xhigh < max < ultra` 的官方顺序并入该条目的 supported 档位并设为默认，避免 Codex 认为该档位对当前模型不可用。
- **逐模型思考档位**（2026-09-22 补充，对齐 cc-switch）：`provider_models.reasoning_levels` 非空时，用声明值**替换**该条目的 `supported_reasoning_levels`（只保留 Codex 认识的档位，函数 `declaredReasoningLevels` 按官方顺序重排、丢弃未知值），`default_reasoning_level` 走三级回落：声明的 `default_reasoning_level`（仍须在支持集内）→ 模板默认档位（`high`，仍须在支持集内）→ 支持集最高档。未声明则整条保留模板默认。cc-switch 的同一算法见 `codex_config.rs`：canonical 档位取交集、默认档按「声明 → 模板 → 最高」回落。
- **`model_catalog_json` 是条件字段：`provider_models` 为空就完全不写**（`adapter.go` 的 `if len(models) > 0`）。此前条件是 `len(models) > 0 || p.Model != ""`，会让「模型映射留空、靠 Codex 自己发现模型」的渠道（如 `axonhub-any`）被凭空造出一份单条目录；设置 `model_catalog_json` 后 Codex 不再拉 `/v1/models`，效果就是"任意模型渠道被锁成一个模型"。cc-switch 的对应行为是：模型映射为空 → 不生成 catalog 文件、不设 `model_catalog_json`。
- **渲染前自检**（`validateCatalogEntry`）：宁可报错也不写出 Codex 会拒绝加载的目录；目录存在时，供应商默认模型（`provider.Model`）始终会出现在目录里，即使它不在 `provider_models` 中（profile 指向目录外的模型会被 Codex 视为未知模型）。目录不存在时 profile 里也不写 `model_catalog_json`，`model` 直接交给 Codex 原生解析。

验证方式：`codex debug models --bundled` 二进制字符串挖掘 + 逐项 live 探针（临时 `CODEX_HOME`、127.0.0.1 SSE 假服务、虚构密钥）。端到端复核：用真实库里的 `axonhub-codex` 记录跑 `codex.New().Render()` + `BuildLaunchInputs()`，把产出的 profile/catalog 交给本机 `codex.exe`（仅把 `base_url` 指向本地假服务、`auth.command` 换成占位环境变量），退出 0、`turn.completed`、`model=gpt-6-astra`、`reasoning.effort=low`（与 profile 一致）、instructions 117 字符、工具集为 `[exec_command, write_stdin, request_user_input, view_image, multi_agent_v1, web_search]`（无 `apply_patch`）。

## 规范修正

遵循 §2/§10 的安全要求，默认通过 `[model_providers.<id>.auth]` 回调 `relay secret get codex <id>`，不把明文凭据写入环境、argv 或配置，也不主动扩大 shell allowlist。`run`/`exec` 在启动前幂等安装主配置注册表和独立 profile，但不修改顶层默认；`switch` 才更新顶层 `model_provider`/`model`。

## 依据

- https://developers.openai.com/codex/config-advanced/ （profile 文件迁移、配置覆盖）
- https://developers.openai.com/codex/config-reference/ （auth.command、env_key、刷新语义与互斥约束）
- 本机 `codex --help`、`codex exec resume --help`、`codex debug models --bundled` 与上述隔离探针。

## 实现边界与验证

- Relay provider ID、Codex provider ID 和独立 profile 文件名统一。profile 不重复供应商注册表；`BuildLaunchInputs` 使用 `--profile <id>`，并在启动前确保注册表/profile 已安装。
- `Extra.codex_config` 接受已解析的原生 TOML 对象，按原 `model_provider` 选择供应商定义；仅映射模型与供应商相关字段，不从导入配置传播执行审批、安全沙箱或 shell 环境策略。`Provider.BaseURL`、`Provider.Model` 优先。
- Provider 经 SQLite JSON 列往返后数字会变为 Go `float64`，直接编码会生成原生整数配置不接受的 `3.0`。适配器对重试、超时、上下文窗口和自动压缩阈值这些已知整数字段恢复整数类型，并拒绝负数、小数或失去精度的浮点值；单元测试和本机假 SSE 测试均覆盖 JSON 往返路径。
- 明文字段（如 `api_key`、`experimental_bearer_token`、`http_headers`）拒绝渲染；默认主凭据走 `auth.command`，`env_key` 仅为显式降级模式。额外 header 仍用 `env_http_headers` 环境变量名映射和加密存储中的 `env:NAME`。配置产物和 argv 不含凭据。
- `ApplyGlobal` 使用 TOML 语法树修改已有顶层模型指针，保留同行注释、无关设置和多行字符串；供应商区块由 `BEGIN/END RELAY CODEX` 注释管理。供应商声明过模型目录时，`switch` 同时把 `model_catalog_json` 写入主配置，确保用户直接启动 `codex`（不带 `--profile`）也能看到模型列表。拒绝覆盖同名的手动供应商或独立 profile。文件按安全写入接口替换，并使用配置锁避免多个 Relay 进程同时修改。
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


## 2026-09-24：回调绑定数据目录

`auth.args` 从 `["secret","get","codex",<id>]` 改为 `["--home",<abs-relay-root>,"secret","get","codex",<id>]`。理由与 Claude Code 侧一致：外部启动方式（Multica、relay-codex 包装）可能不带 `--home`，不传则会解析到默认 `~/.relay`，同名供应商下可能读错密钥。Codex 的 auth 是 argv 数组形式，不经 shell 解析，无需转义。
