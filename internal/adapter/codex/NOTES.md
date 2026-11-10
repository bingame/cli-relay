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
- 第三方 `/responses` 网关会拒绝 Codex 的 freeform `apply_patch`（`type=="custom"`）工具，因此 Relay 的目录条目不声明 `apply_patch_tool_type` / `web_search_tool_type` / `tools` / `model_messages`，改用 `shell_type = "shell_command"`。这与 cc-switch 的实现做法一致：cc-switch 为每个非官方模型克隆中性模板、覆写 slug/display_name/description/context_window/priority(1000+序号)，并把 `base_instructions` 保留为一句中性描述。
- Relay 的实现取同一形态：`catalog.go` 里内置一份中性模板（slug/display_name/description 为 `relay-template`，`base_instructions` 为 cc-switch 使用的同一句 117 字符中性提示词，`default_reasoning_level = high`，`supported_reasoning_levels` 为 none/high），逐模型克隆后覆写。差异：`context_window`/`max_context_window` 优先取 `provider_models.context_window`，其次取 `Extra.codex_config.model_context_window`，都没有时用 128000；条目里声明的 `model_reasoning_effort` 会按 `none < minimal < low < medium < high < xhigh < max < ultra` 的官方顺序并入该条目的 supported 档位并设为默认，避免 Codex 认为该档位对当前模型不可用。
- **逐模型思考档位**（2026-09-22 补充，对齐 cc-switch）：`provider_models.reasoning_levels` 非空时，用声明值**替换**该条目的 `supported_reasoning_levels`（只保留 Codex 认识的档位，函数 `declaredReasoningLevels` 按官方顺序重排、丢弃未知值），`default_reasoning_level` 走三级回落：声明的 `default_reasoning_level`（仍须在支持集内）→ 模板默认档位（`high`，仍须在支持集内）→ 支持集最高档。未声明则整条保留模板默认。cc-switch 的同一算法见 `codex_config.rs`：canonical 档位取交集、默认档按「声明 → 模板 → 最高」回落。
- **`model_catalog_json` 是条件字段：`provider_models` 为空就完全不写**（`adapter.go` 的 `if len(models) > 0`）。此前条件是 `len(models) > 0 || p.Model != ""`，会让「模型映射留空、靠 Codex 自己发现模型」的渠道被凭空造出一份单条目录；设置 `model_catalog_json` 后 Codex 不再拉 `/v1/models`，效果就是"任意模型渠道被锁成一个模型"。cc-switch 的对应行为是：模型映射为空 → 不生成 catalog 文件、不设 `model_catalog_json`。
- **渲染前自检**（`validateCatalogEntry`）：宁可报错也不写出 Codex 会拒绝加载的目录；目录存在时，供应商默认模型（`provider.Model`）始终会出现在目录里，即使它不在 `provider_models` 中（profile 指向目录外的模型会被 Codex 视为未知模型）。目录不存在时 profile 里也不写 `model_catalog_json`，`model` 直接交给 Codex 原生解析。

验证方式：`codex debug models --bundled` 二进制字符串挖掘 + 逐项 live 探针（临时 `CODEX_HOME`、127.0.0.1 SSE 假服务、虚构密钥）。端到端复核：用真实库中的第三方供应商记录跑 `codex.New().Render()` + `BuildLaunchInputs()`，把产出的 profile/catalog 交给本机 `codex.exe`（仅把 `base_url` 指向本地假服务、`auth.command` 换成占位环境变量），退出 0、`turn.completed`、模型和推理档位均正确、工具集无 `apply_patch`。

## 规范修正（v0.13 前的历史实现，当前行为见文末）

遵循 §2/§10 的安全要求，默认通过 `[model_providers.<id>.auth]` 回调 `relay secret get codex <id>`，不把明文凭据写入环境、argv 或配置，也不主动扩大 shell allowlist。`run`/`exec` 在启动前幂等安装主配置注册表和独立 profile，但不修改顶层默认；`switch` 才更新顶层 `model_provider`/`model`。

## 依据

- https://developers.openai.com/codex/config-advanced/ （profile 文件迁移、配置覆盖）
- https://developers.openai.com/codex/config-reference/ （auth.command、env_key、刷新语义与互斥约束）
- 本机 `codex --help`、`codex exec resume --help`、`codex debug models --bundled` 与上述隔离探针。

## 实现边界与验证（v0.13 前的历史记录）

- Relay provider ID、Codex provider ID 和独立 profile 文件名统一。profile 不重复供应商注册表；`BuildLaunchInputs` 使用 `--profile <id>`，并在启动前确保注册表/profile 已安装。
- `Extra.codex_config` 接受已解析的原生 TOML 对象，按原 `model_provider` 选择供应商定义；仅映射模型与供应商相关字段，不从导入配置传播执行审批、安全沙箱或 shell 环境策略。`Provider.BaseURL`、`Provider.Model` 优先。
- Provider 经 SQLite JSON 列往返后数字会变为 Go `float64`，直接编码会生成原生整数配置不接受的 `3.0`。适配器对重试、超时、上下文窗口和自动压缩阈值这些已知整数字段恢复整数类型，并拒绝负数、小数或失去精度的浮点值；单元测试和本机假 SSE 测试均覆盖 JSON 往返路径。
- 明文字段（如 `api_key`、`experimental_bearer_token`、`http_headers`）拒绝渲染；默认主凭据走 `auth.command`，`env_key` 仅为显式降级模式。额外 header 仍用 `env_http_headers` 环境变量名映射和加密存储中的 `env:NAME`。配置产物和 argv 不含凭据。
- `ApplyGlobal` 使用 TOML 语法树修改已有顶层模型指针，保留同行注释、无关设置和多行字符串；供应商区块由 `BEGIN/END RELAY CODEX` 注释包围。供应商声明过模型目录时，`switch` 同时把 `model_catalog_json` 写入主配置，确保用户直接启动 `codex`（不带 `--profile`）也能看到模型列表。拒绝覆盖同名的手动供应商或独立 profile。文件按安全写入接口替换，并使用配置锁避免多个 Relay 进程同时修改。
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

## 2026-11-03：统一原生 custom 与配置深合并

用户在 Codex 0.166.0 的 TUI 恢复 cc-switch 会话时收到 `modelProviderMismatch`，数据为 `modelProvider=custom`、`currentModelProvider=axonhub`。Relay 现在对第三方 Codex 供应商固定使用原生 `custom`；Relay ID、profile 文件名、模型目录、环境变量和回调中的供应商索引仍按原 ID 独立。已有 JSONL 不做改写，旧版 Relay 独立原生 ID 会话仍需旧配置或 Handoff。

实测不能用“profile 中重复完整表”保证替换：主配置含 `auth`、profile 含 `env_key` 时，两个字段会深合并并触发互斥校验，反方向相同。仅加 `-c model_providers.custom={完整定义}` 也不会删除下层字段。当前实现将 `model_providers.custom=false` 写入 profile 层，再在 CLI 层写入完整无密钥表；类型切换先移除旧表，再恢复当前定义。两次 `-c` 会先合为同一 CLI 层，不能替代 profile 与 CLI 的分层。

源码依据：[merge.rs](https://github.com/openai/codex/blob/f58acd6ede312266a7a0eaa63fd8dadacb4de239/codex-rs/config/src/merge.rs) 的 `merge_toml_values_at_path`（表递归合并，其他类型替换）、[overrides.rs](https://github.com/openai/codex/blob/f58acd6ede312266a7a0eaa63fd8dadacb4de239/codex-rs/config/src/overrides.rs) 的 CLI 层汇总。官方 [profiles 文档](https://learn.chatgpt.com/docs/config-file/config-advanced#profiles) 说明主配置后 overlay profile，但没有承诺供应商整表替换。

当前 `BuildLaunchInputs` 只安装独立重置 profile，返回 `--profile <relay_id> -c model_providers.custom={完整定义}`，不改主配置。外部启动器必须使用整个 `render-args` 数组；单独使用该 profile 因缺少完整表覆盖会报类型错误。未设置的模型选项仍按 Codex 原生规则继承，包括无显式目录时可能继承主配置目录。`ApplyGlobal` 允许定点接管 cc-switch 的标准 custom 表及分散子表，替换单个固定管理槽，保留其他 Provider、项目和注释；不支持安全改写的内联 custom 表拒绝处理。同名手动 profile 仍拒绝覆盖。

本机 npm 原生 `codex.exe --version` 为 **0.166.0**，虚构凭据、临时 HOME/CODEX_HOME、两个本地 SSE 假服务验证通过：

- 主配置无 custom、主配置 auth → 临时 env_key、主配置 env_key → 临时 auth 三组均收到正确模型/鉴权并完成回合。
- `switch` 清除旧默认 profile 指针后，直接启动不带 profile 的 Codex 完成回合。
- 使用实际 `BuildLaunchInputs`，A 创建持久会话、B `exec resume` 同会话；B 服务收到自身凭据/模型、A 的用户历史和助手回复。额外 `app-server thread/resume` 读取同会话成功；该 RPC 探针未复现 TUI 的独立 ID 拒绝行为，因此不作为错误对照证明。

隔离测试显式将 HTTP/HTTPS/ALL_PROXY 也指向假服务（服务不转发），覆盖本机 Windows 系统代理，避免本地请求收到系统代理的 502。测试不修改真实配置或请求真实模型。

**另一个上游差异**：0.166.0 的 `app-server` 拒绝顶层 `--profile`（错误列出支持 runtime 命令，不含 app-server）。所以当前完整 `render-args` 不能直接用于此版本 app-server，历史 Multica 参数核对不代表该版本已兼容。这项入口适配留待独立处理；本轮恢复 RPC 探针在无主配置的临时目录用 CLI 选择器与完整定义直接启动，CLI 创建/续接则使用真实完整 profile 参数。

运行所有本机探针：`$env:RELAY_CODEX_INTEGRATION='1'; go test ./internal/adapter/codex -run '^TestLocalCodex' -v`。

## 管理标记与 cc-switch 的冲突（2026-11-04）

**事故**：`relay run codex --provider axonhub-lite` 报 `relay: Codex Relay 管理标记不完整`，而同一份 `config.toml` 下的 `axonhub-unstable` 正常。

**根因**：本机 `~/.codex/config.toml` 里残留了一个孤立的 `# END RELAY CODEX axonhub-lite`——对应供应商的 BEGIN 和 `[model_providers.axonhub-lite]` 表都已不在，只剩这一行 END，位置紧贴在 `# BEGIN RELAY CODEX axonhub-prod` 之前。`removeManagedBlock` 按 provider 精确匹配标记，遇到 `END` 无 `BEGIN` 就走 `start == -1 || finish == -1` 分支直接报错；由于该函数在 `BuildLaunchInputs → install(setDefault=false)` 中被无条件调用，连"根本没写全局配置"的 `run` 也被阻断。其他 provider 的标记配对完整，所以只有 `axonhub-lite` 失败。

**证据**：用 `CODEX_HOME` 指向 `config.toml` 的隔离副本复现，删掉该行后 `axonhub-lite` 与 `axonhub-unstable` 均启动成功；回溯 `config.toml.bak`（cc-switch 落盘 2026-11-02 00:37）可见同样的错配（`# BEGIN RELAY CODEX axonhub-any` 对应 `# END RELAY CODEX axonhub-lite`），cc-switch 数据库里的 codex 供应商快照也留着 `BEGIN axonhub-prod` / `END axonhub` 一类错位，说明是 cc-switch 整份重写配置时把 Relay 的成对标记拆散并反复回流。

**修复**：标记降级为纯注释，不再参与定位。

- `removeManagedMarkers` 只删除属于本次供应商的标记行（共享原生 ID `custom` 与旧版按供应商 ID 命名的形式都清理），成对、孤立、重复、顺序颠倒一视同仁，字符串内的相同文字不受影响。
- 活动槽的删除与替换始终由 `removeProviderTable` 按 TOML 结构完成，标记残缺或错位都不会牵连其他供应商表；`removeProviderTable` 的删除改为整行 + 保留同行尾注释，避免留下空格空洞。
- 追加块前只对追加位置做 `trimTrailingBlankLines`，用户文件其他位置的排版不动；重复 `switch` 逐字节幂等。
- 回归测试：`TestApplyGlobalToleratesBrokenMarkers`（孤立 END/BEGIN、倒序、重复配对）、`TestRemoveManagedMarkersOnlyRemovesMarkers`（含旧版命名）、`TestApplyGlobalDoesNotDeleteBehindMisplacedMarkers`（标记包住别的表时不得误删）。

## 2026-11-05：Multica app-server 参数复核

本机 Codex 0.166.0、Multica CLI 0.8.0；使用虚构凭据、隔离 HOME/CODEX_HOME/Relay 数据目录和不转发的本地 HTTP 假服务验证，没有修改真实配置或请求真实模型。

- `codex app-server --profile <id>` 退出 2，报 `unexpected argument '--profile'`；将参数前置为 `codex --profile <id> app-server` 也不能使用，实际启动退出 1，报 `--profile only applies to runtime commands ...`。带 `--help` 时可能提前返回帮助并退出 0，不能据此判断启动兼容。
- `-c 'model_providers.<id>={ name="...", ... }'` 的合法 TOML 内联表可以直接使用。`initialize` 和 `config/read` 均成功，供应商被解析为对象；逐字段 `-c model_providers.<id>.<field>=...` 也成功，并非唯一可用写法。
- 对同一内联表额外进行字符串编码后，再作为 `-c` 赋值，启动退出 1，复现 `invalid type: string ... expected struct ModelProviderInfo`。该报错不能证明 Codex 不接受内联表；应先检查 TOML 语法和额外引号。官方 [Advanced Configuration](https://developers.openai.com/codex/config-advanced/) 说明 `--config` 值按 TOML 解析，解析失败时回落为字符串。
- 与 Multica 配置相同的 `model_provider=<id>`、完整内联供应商表和嵌套 `auth.command/args` 结构，在本地假服务上通过 `initialize → thread/start → turn/start → turn/completed`。回调从隔离 Relay 密钥库获取虚构凭据，假服务验证鉴权正确，模型为 `gpt-6.1-sol`，推理等级为 `xhigh`。这只验证配置和回调机制，不代表真实供应商已经通过可用性验证。
- 当前已安装 Relay 的隔离 `render-args` 探针输出 `--profile <id> -c model_providers.custom={完整定义}`，与当前源码一致，并非仅输出 `--profile <id>`。它仍含 app-server 不支持的 profile 参数，因此不能直接填入 Multica 的 `custom_args`。本次只记录验证结论，不实现该入口的参数适配。

本地 Multica 的 `server/pkg/agent/codex.go` 先构造 `app-server --listen stdio://`，再追加归一化后的 `custom_args`；`claude.go` 中的 `unshellQuoteArg` 保留普通 TOML 赋值内部的引号。模型和推理等级另通过 RPC 传递，不能只看 provider 的默认模型来判断最终请求模型。

## 2026-11-05：app-server 渲染入口与隔离回合验证

- 新增 `render-args/render-env --runtime app-server`，默认 `cli` 路径不变。app-server 输出完整供应商内联表与模型设置的纯 `-c` 数组；包装模式也自动选择这条路径，不安装 profile、不改主配置。
- 本机 0.166.0 的 `initialize` 成功不代表配置有效：主配置 `custom.env_key` 与 CLI `custom.auth` 深合并时，`config/read` 报 `provider auth cannot be combined with env_key`。覆盖整个 `model_providers` 内联表也不能清理旧字段。`-c profile="probe"` 则因 legacy profile 配置被拒绝，不能绕过入口限制。
- 因此 app-server 使用 `relay_<relay_id>` 独立原生 ID，避开 cc-switch/全局 `custom` 槽；其回调仍读取原 Relay ID 的凭据。普通 CLI 继续使用 `custom`。两种身份及历史手工 Agent 身份不同，已有会话不承诺直接 resume；需要时使用 Handoff。
- `scripts/verify_codex_relay.py` 使用隔离 Relay/CODEX_HOME、虚构凭据、本地不转发假 SSE 服务和显式假服务代理，覆盖 callback、env_key、Multica 风格 wrapper。三者均通过 `initialize → config/read → thread/start → turn/start → turn/completed → thread/read`；假服务确认正确鉴权、RPC 覆盖后的 Agent 模型和 `xhigh`，主配置及所有 profile 逐字节不变。主配置分别预置另一种鉴权的 `custom` 槽，未混入所选供应商。
- 直接调用本地 Multica 源码导出的 `NormalizeCodexLaunchArgs`，新渲染数组与现有 Agent 的完整内联表数组均逐项保持不变；同时通过该仓库的 `TestNormalizeCodexLaunchArgsStripsShellQuotes` 和 `TestBuildCodexArgsExtraArgsBeforeCustomArgsAndFiltersBoth`。Relay 全量 `go test ./...`、`go vet ./...` 通过。
- 实际回读的 `worker-libinpc-unstable` 当前模型为 `gpt-6.1-sol`，参数为合法内联表，原生供应商 ID 为 `axonhub-unstable`，回调无明文凭据。当前 Relay 供应商默认模型为 `gpt-6-astra`，不能据此推断 Agent 最终模型。本次未修改服务端 Agent、未请求真实供应商；隔离回合不构成真实渠道可用性证明。

## 2026-11-05 补：真实主配置下的深合并与身份校验

上一节用的是"另一份鉴权的 `custom` 槽"，没有覆盖本机 `~/.codex/config.toml` 里的真实形态。本轮把主配置原样复制到临时 `CODEX_HOME`（真实配置本身不变）复核，结论更严格：

- **`custom` 槽在本机不可用**。真实主配置的 `[model_providers.custom]` 带 `experimental_bearer_token`，而 CLI 内联表要写 `auth`。两者在 CLI 层深合并后触发 `provider auth cannot be combined with experimental_bearer_token`，`config/read` 与 `--print-startup-config` 都失败。把 `experimental_bearer_token` 覆盖成空串也无济于事——空串不等于删除，合并后字段仍在。逐字段只覆盖 `base_url` 又会因缺 `name` 报 `provider name must not be empty`。两次 `-c`（先 `=false` 再写表）同样不行：同一 CLI 层先落到 `false`，直接报 `invalid type: boolean false, expected struct ModelProviderInfo`。所以 **app-server 走独立 `relay_<relay_id>` 命名空间是必须的，不是可选项**。
- 相反，**用一个主配置里没有被凭据污染的独立 ID 就能通过**。用 Agent 当前那套 `model_provider="axonhub-unstable"` + 完整内联表，在真实主配置副本上 `thread/start → turn/start → turn/completed` 全程成功，假上游收到 `Bearer <回调凭据>`、`model: gpt-6.1-sol`、`reasoning.effort: xhigh`；换成 `relay_axonhub-unstable` + 模型 `gpt-6-luna` 同样成功。
- **供应商身份校验在 RPC 层**。同一线程换进程 `thread/resume` 时：不传 `modelProvider` 成功，显式传匹配的 `relay_axonhub-unstable` 成功，传 `axonhub-unstable` 失败并返回 `modelProviderMismatch`（`currentModelProvider` 为 `relay_...`）。`turn/start` 同理，首次用错 ID 会直接拒绝该回合。这条决定了"换标签就要换历史身份"：普通 CLI 的 `custom`、旧手工 Agent 的 `axonhub-unstable`、以及 app-server 的 `relay_axonhub-unstable` 是三个互不相认的身份，跨身份续接只能走 Handoff。
- **模型以 RPC 为准**。供应商默认 `gpt-6-astra`、`render-args` 里写的 `gpt-6-luna`、Agent 字段 `gpt-6.1-sol` 三者并存时，最终请求用的是 `thread/start` 传入的那个，命令行里的 `-c model=` 只作默认值。核对 Agent 是否生效必须回读 `multica agent get`，不能看 `render-args`。
- **已安装的 Relay 还没有这个入口**。本机 `relay.exe` 为 `v0.1.6-0.20261025184041-9589a5c00e50+dirty`，`render-args --runtime app-server` 报 `unknown flag: --runtime`，只输出旧的 `--profile <id> -c model_providers.custom={...}`。源码侧 `go run ./cmd/relay provider render-args codex axonhub-unstable --runtime app-server` 输出四条 `-c`（独立 ID 表、`model`、`model_provider`、`model_reasoning_effort`），本轮上述真实主配置回合用的就是这套参数。要落到 Multica 需重新安装 Relay。

探针留存在临时目录（`probe_appserver.py` 读回解析结果、`probe_turn*.py` 跑回合、`probe_resume.py` 验证身份校验、`probe_auth.py` 只回传鉴权长度不打印凭据），均使用临时 `CODEX_HOME`、本地假 SSE 服务和 `NO_PROXY=127.0.0.1`，未请求真实供应商，也未修改服务端 Agent。

## 2026-11-05：TUI 切换模型后无法保存默认值（历史复现，后续修复见下一节）

用户经 `relay run codex` 启动后切换模型，收到 `Failed to save default model`、`config/batchWrite`、`invalid type: boolean false, expected struct ModelProviderInfo in model_providers.custom`，仍可继续对话。

**原因**：上述重置 profile 在最终启动配置中会被 CLI 的完整表覆盖，但 profile 文件自身不是有效的 `ConfigToml`。Codex 保存配置时先单独校验可写用户层，再校验合并结果；选择 profile 时可写用户层就是 `<relay_id>.config.toml`。因此模型回合可正常执行，默认模型和推理档位的保存会在写文件之前失败。这是 Relay 生成配置与 Codex 保存契约不兼容，不是用户手写了错误配置，也不是供应商拒绝模型。仅手改 profile 不能持久解决：下一次 Relay 启动会重新生成。

依据：官方 [Advanced Configuration](https://developers.openai.com/codex/config-advanced/) 的独立 profile 层与 CLI 覆盖规则、[app-server](https://developers.openai.com/codex/app-server/) 的 `config/batchWrite`；本次检查的上游源码提交为 `e7f32cf1928a35a9232a633a51c2a03b5a7e2c72`，`config/src/state.rs::get_active_user_layer` 明确返回原始可写层，`app-server/src/config_manager_service.rs::apply_edits` 先调用 `validate_config(&user_config)`，之后才校验 `updated_layers.effective_config()`。TUI 的 `app/model_defaults.rs` 先更新活跃线程模型，再调用保存，所以保存失败不撤销当前回合的选择。

**本机 0.166.0 隔离复现**：`TestLocalCodexModelSelectionPersistence` 使用真实适配器生成的 profile 和 CLI 定义；因 app-server 不支持 `--profile`，将 profile 放到临时 `CODEX_HOME/config.toml`，复用相同的可写用户层校验路径。`initialize`、`config/read` 成功，随后保存模型及推理档位的 `config/batchWrite` 返回 `-32600` / `configValidationError`，配置逐字节不变。对照组使用实际 `ApplyGlobal` 生成的完整配置、不传 profile；保存返回 `ok`，文件解析后确认模型和 `high` 已持久化、供应商身份仍为 `custom`。此测试验证 RPC 保存路径，不宣称自动操作过 TUI；测试未执行凭据回调、未请求模型、未修改真实配置。

**可用绕过方式**：默认 callback 模式下，显式执行 `relay switch <供应商> --target codex` 后直接启动 `codex` 或 `codex resume <会话ID>`，不再经 `relay run` 加载重置 profile。这会改变全局默认供应商；需要多个供应商并行且不改默认时，可继续现有 Relay 会话，但本次选择不会保存成默认值。`env_key` 模式的直接启动还需要对应凭据环境，不能把 callback 的直接启动说明当作无条件保证。

**修复边界**：不能仅删掉 `false` 或改成完整表来宣称修复。隔离主配置含 `custom.env_key` 时，profile 的 `custom.auth` 仍被深合并并报互斥错误；把 `env_key` 写成空串也不能删除。回退独立原生 ID 会失去已有 `custom` 会话身份兼容；改用独立 `CODEX_HOME` 则需要明确处理原配置继承、Skills/MCP、会话发现与保存位置。当前保留既有启动语义并记录限制，不能将此前通过的 `exec`/`resume` 验证称作 TUI 配置保存兼容。

验证命令：`$env:RELAY_CODEX_INTEGRATION='1'; go test ./internal/adapter/codex -run '^TestLocalCodexModelSelectionPersistence$' -v -count=1`。


## 2026-11-06：合法独立用户层修复模型保存

用户确认采用「独立配置、共享原会话和 Skills、模型选择按供应商保存」。普通 CLI 的 BuildLaunchInputs 改为设置供应商独立 CODEX_HOME 与共享 CODEX_SQLITE_HOME，不再加载旧 false profile、不覆盖原主配置，也不改变原生 custom 身份。仅在内存中继承原配置后整体替换 provider 表；MCP 静态 header/凭据环境变量经环境引用传递，旧 provider 的 token 不进入独立配置。模型与推理档位只在对应生成默认未变化时保留 TUI 保存值；其余用户设置每次从主配置刷新。

独立目录位于原生目录的 `.relay/<Relay目录摘要>/<供应商ID及摘要>/`，不同 Relay 数据库不串配置，嵌套 Relay 启动会解析回原始目录。Windows 用无特权 Junction 共享会话/Skills/插件等目录、硬链接共享现有指令与认证文件；源文件原子替换时，下次启动重新绑定硬链接。SQLite 通过官方 sqlite_home/CODEX_SQLITE_HOME 指回原目录，不分别链接数据库与 WAL。daemon 和临时文件保持独立。硬链接在当前进程期间不会自动跟随源文件的原子替换；用户修改主配置/指令后应重新启动。

本机 **Codex 0.166.0**、临时 HOME/USERPROFILE/CODEX_HOME、虚构凭据和 127.0.0.1 假服务实测：

- `TestLocalCodexModelSelectionPersistence`：固定旧 false 输入仍复现 -32600；实际新适配器的 callback/env_key 两种配置均经 `initialize → config/read → config/batchWrite` 成功保存模型与 high，再次 BuildLaunchInputs 保留选择，原主配置不变；`skills/list` 可发现共享用户 Skill。
- `TestLocalCodexIntegration`：覆盖主配置旧 auth 与新 env_key、旧 env_key 与新 callback，两方向均只使用选中凭据/模型完成回合。
- `TestLocalCodexResumeAcrossProviders`：A 创建 custom 会话，B 的 `thread/list` 可发现、显式 `modelProvider=custom` 的 `thread/resume` 可恢复、`exec resume` 携带 A 历史且只使用 B 凭据；rollout 保存在原 sessions。`thread/list` 默认仅列 cli/vscode，测试创建的是 exec 会话，因此必须指定 `sourceKinds=["exec"]`；这属于列表过滤，不是丢失会话。
- 单元测试覆盖重新启动、显式默认变更、嵌套启动、并发供应商、共享文件、MCP 凭据不落新配置。RPC 验证保存接口，未声称自动点击过 TUI。

依据：官方 [配置参考](https://developers.openai.com/codex/config-reference/) 的 sqlite_home、MCP 环境引用及 provider 鉴权互斥规则；[Advanced Configuration](https://developers.openai.com/codex/config-advanced/) 的 CODEX_HOME；[app-server](https://developers.openai.com/codex/app-server/) 的 config/batchWrite 与 thread/list sourceKinds。

普通 CLI 的 `render-args` 现在为合法空数组，必须同时应用 `render-env` 的目录环境变量；默认 callback 仍不在其中输出主凭据。现有 `--runtime app-server` 及 Multica wrapper 保留独立 relay_<id> 的纯 -c 方式。旧 false profile 不再加载，无需修用户主配置；正在运行的旧进程须重启。
