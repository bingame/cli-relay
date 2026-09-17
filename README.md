# Relay

Relay 是 Go 编写的本地命令行工具，为 Claude Code、Codex 选择供应商，提供单次执行监管和跨 CLI 的 Markdown 会话交接。没有常驻服务，不包含任务队列或自动重试循环。

已实现规范 P0–P3 与 P5 的接入能力。P4 的通知、重试、关机仅预留接口，不自动执行这些操作。原生 Claude Code / Codex 仍需单独安装。

## 安装

发行产物为无运行时依赖的预编译二进制，支持 Linux/macOS amd64、arm64 与 Windows amd64。安装器自动校验 SHA-256、配置 PATH，并为本机 Claude Code/Codex 安装 Handoff Skill。

**当前从私有仓库 `bingame/cli-relay` 分发。** 有仓库访问权限并已 `gh auth login` 的用户可从最新稳定 Release 一行安装，不必准备新域名：

```sh
gh release download --repo bingame/cli-relay --pattern install.sh --output - | RELAY_DOWNLOAD_MODE=gh sh
```

```powershell
$env:RELAY_DOWNLOAD_MODE='gh'; gh release download --repo bingame/cli-relay --pattern install.ps1 --output - | iex
```

PowerShell 安装器保持纯 ASCII 源码，避免 `gh --output -` 进入原生管道时按系统代码页错误解码 UTF-8 中文并破坏脚本语法。私有下载复用 GitHub CLI 的登录态，不要求把 token 写到命令或文件。`gh` 仅用于私有仓库下载，Relay 本身无运行时依赖。软件源和发布配置见 [发布说明](distribution/NOTES.md)。下列 `curl`/`irm`、Homebrew/Scoop 和匿名 `go install` 路径需要公开发行资源，当前私有部署不宣称它们已上线。

Linux / macOS（公开发行后可直接使用仓库地址）：

```sh
curl -fsSL https://github.com/bingame/cli-relay/releases/latest/download/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://github.com/bingame/cli-relay/releases/latest/download/install.ps1 | iex
```

Unix 默认 `~/.local/bin`，Windows 默认 `%LOCALAPPDATA%\Relay\bin`。Unix 当前终端按安装输出执行 `export` 或重开终端；Windows 安装完成即可在当前 PowerShell 使用。

已有 Homebrew/Scoop 的用户，在软件源完成配置后可使用：

```sh
brew install bingame/tap/relay
```

```powershell
scoop bucket add relay https://github.com/bingame/scoop-bucket
scoop install relay
```

也可从 Releases 手动下载对应归档，按 `checksums.txt` 校验后解压。使用 Go 1.26+ 的开发者可运行：

```sh
go install github.com/bingame/cli-relay/cmd/relay@latest
relay skill install
```

`go install` 或手动解压不会执行安装后钩子。重复运行一行命令可升级；设置 `RELAY_VERSION=v0.1.0` 可固定版本，`RELAY_INSTALL_DIR` 可指定绝对安装目录。更多安装选项见发布说明。

## 源码构建与快速开始

需要 Go 1.26 或更新版本。SQLite 使用 `modernc.org/sqlite`，无需 CGO。

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -o bin/relay.exe ./cmd/relay
.\bin\relay.exe --help
.\bin\relay.exe skill install

# 先查看导入报告，不写入 Relay 数据目录
.\bin\relay.exe provider import --from cc-switch .\providers.sql --dry-run
.\bin\relay.exe provider import --from cc-switch .\providers.sql
.\bin\relay.exe provider list

# 换成报告中的供应商 ID；导入本身不会自动 switch
.\bin\relay.exe run codex --provider my-provider -- "检查当前项目"
.\bin\relay.exe exec codex --provider my-provider -- "运行测试并报告结果"
.\bin\relay.exe switch my-provider --target codex
.\bin\relay.exe status
```

Linux/macOS：`CGO_ENABLED=0 go build -trimpath -o bin/relay ./cmd/relay`。
生成的 Relay 二进制没有 Go、Node、Python 或 SQLite 运行时依赖。`scripts/` 中的验证脚本仅供开发使用。

手动添加支持重复 `--target`。API key 从 stdin 输入，不能放入 argv：

```sh
read -r -s -p 'API key: ' RELAY_INPUT_KEY; printf '\n'
printf '%s' "$RELAY_INPUT_KEY" | relay provider add \
  --id example --target codex --base-url https://api.example.com/v1 \
  --model example-model --api-key-stdin
unset RELAY_INPUT_KEY
```

## 命令

| 命令 | 行为 |
| --- | --- |
| `provider add/list/remove` | 管理供应商；删除仍被 current 引用的供应商会报错 |
| `provider import --from cc-switch <sql> [--dry-run]` | 内存执行受限 SQL dump、解析真实列、加密凭据和原始快照；冲突 ID 自动加 `-imported-N` |
| `provider render-args <cli> <id>` | 输出原生参数 JSON 数组，不包含密钥 |
| `provider render-env <cli> <id> [--format dotenv\|json]` | 输出启动环境；默认 `KEY=VALUE`，Multica 使用 JSON |
| `switch <id> [--target <cli>]` | 合并原生非敏感配置并修改指定 target 的 current 指针 |
| `run <cli> [--provider <id>] -- ...` | 原生交互；Unix 使用 `syscall.Exec`，Windows 继承控制台后等待子进程 |
| `exec <cli> [--provider <id>] -- ...` | 单次无头运行、实时转发输出、记录会话、返回分类退出码 |
| `status` | 输出 current 和仍存活的管理实例；不是完整系统进程枚举 |
| `skill install [--cli claude-code,codex]` | 从二进制离线安装 Handoff Skill，默认只安装本机可探测到的 CLI |
| `--version` | 显示发布版本 |
| `handoff schema [--validate <doc>]` | 输出 JSON Schema，或校验 Markdown 文档 |
| `handoff export --cli <cli> --live` | 输出应交给活 agent 的请求；不会假装读取活 agent 的内存 |
| `handoff export --cli <cli> --input <doc或-> [-o <path>]` | 校验并接收活 agent 实际生成的文档 |
| `handoff export --cli <cli> --dead --session <id> [-o <path>]` | 官方结构化读取/恢复，失败后原始文件解析与模型总结 |
| `handoff continue --doc <path> [--cli <cli>] [--provider <id>] [--exec]` | 新会话注入文档，单独再次强调硬约束 |

不指定 provider 时使用该 CLI 的 current；没有 current 时沿用原生 CLI 默认配置。`--` 后参数由原生 CLI 消费。无头模式会补齐必要输出标志；冲突输出格式报错。Codex 的 `app-server` 保持双向协议并返回原生退出码，由 Multica 处理交互和重试。

普通 `exec`：`0` 成功，`10` 可重试的基础设施故障，`11` 需要人工回答，`12` 历史损坏/恢复被拒绝，其他非零为未分类错误。Relay 不自动回答问题。分类依赖上游事件及错误文字，不声称覆盖所有未来版本。

## 原生配置差异

**Claude Code：为了避免全局 settings 覆盖本次供应商凭据，启动使用 `--setting-sources ""`。本机 2.1.268 实测此选项也关闭 `CLAUDE.md` 自动加载。** 需要项目指令时明确传入，例如：

```sh
relay run claude-code --provider example -- --append-system-prompt-file ./CLAUDE.md
```

导入的非敏感 Claude settings 经渲染文件传入；API key 和自定义敏感 header 只通过环境注入。`switch` 用 `.relay-managed.json` 记录管理字段，保留其他用户字段，发现管理字段被手工改动时拒绝覆盖。

Codex 默认使用 `-c` 内联非敏感供应商定义，未执行 switch 也可临时运行。可选 `--codex-launch-mode profile` 或 `RELAY_CODEX_LAUNCH_MODE=profile`；需先 switch 安装独立 profile。Codex 0.134.0 起 profile 已改为 `<name>.config.toml`，不再使用规范初稿中的 `[profiles.name]`。shell 环境过滤不影响 Codex 自身 `env_key` 鉴权，Relay 不主动放宽 shell allowlist。

`switch` 不把密钥写入原生配置。需要 Relay 保存的凭据时，应通过 `relay run/exec` 启动，或把 `render-env` 输出交给受信任的调用进程；单独启动原生 CLI 不会自动从 Relay 解密密钥。

多 target switch 逐个应用并逐个记录成功项，不提供跨原生配置文件的全有或全无事务。切换失败后可用 `status` 查看已完成项。

详细探测依据：[Codex NOTES](internal/adapter/codex/NOTES.md)、[Claude NOTES](internal/adapter/claudecode/NOTES.md)、[导入器 NOTES](internal/importer/ccswitch/NOTES.md)。

## Handoff

一行安装器、Homebrew 和 Scoop 会自动安装随二进制内嵌的 Skill。后续安装了新的 CLI，或需要单独更新时运行：

```sh
relay skill install
relay skill install --cli claude-code,codex
```

默认安装到 `~/.claude/skills/relay-handoff/SKILL.md` 和 `~/.codex/skills/relay-handoff/SKILL.md`，支持 `CLAUDE_CONFIG_DIR` / `CODEX_HOME`。显式 `--cli` 不要求目标 CLI 已安装；自动探测只检查入口，不启动模型。没有目标 CLI 时给出提示。已有同名手动文件或被编辑过的内容会保留并报错，备份移走该文件后可重试；未手改的 Relay Skill 自动升级。

原生 Claude 活会话使用 `/relay-handoff`；通过 Relay 指定供应商启动 Claude 时，隔离设置也会关闭用户 Skill 发现，因此 Relay 显式加载自己的插件，使用 `/relay:relay-handoff`。Codex 使用 `$relay-handoff`，也可直接请求使用该 Skill 交接。已运行的会话若尚未发现新 Skill，请重新启动会话。文档固定包含 YAML 元数据和 8 个中文章节，不能是空模板；可用 `relay handoff schema --validate handoff.md` 检查。

```sh
relay handoff export --cli codex --input handoff-from-agent.md -o handoff.md
relay handoff continue --doc handoff.md --cli claude-code --provider other-provider

relay handoff export --cli codex --dead --session SESSION_ID -o handoff.md
relay handoff continue --doc handoff.md --cli codex --exec
```

退出会话优先使用 Codex 官方 `app-server thread/read` 读取历史并生成摘要；不可用则使用原生无头 resume。最后才容错解析原始 JSONL，忽略损坏行、图片和 thinking，保留已识别的用户原文。来源角色独立 JSON 编码，工具输出不能伪造用户记录。降级文档显式声明可能丢失信息。

可用 `--raw-file <jsonl>` 指定第三层来源，用 `--summary-cli` / `--summary-provider` 选择可用总结模型，`--timeout` 设置每次恢复/总结时限。若没有可用模型，命令报错，不伪造一份“已总结”的文档。这些生成命令可能产生实际模型调用费用。超长交接使用 `continue --exec` 通过 stdin 注入；交互模式保持 TTY，以原生 prompt 参数注入并限制长度。

## Multica 集成

已核对本地 Multica 源码：Codex 后端使用 `app-server --listen stdio://`；`custom_args` 是 JSON 数组，`custom-env-file` 和 `custom-env-stdin` 都要求 **JSON 对象**，不是 dotenv。

推荐只使用渲染器：

```sh
relay provider render-env codex example --format json | \
  multica agent create --name relay-agent --runtime-id codex \
  --custom-args "$(relay provider render-args codex example)" \
  --custom-env-stdin
```

`render-env` 是敏感输出，不要打印到共享终端、CI 日志或长期文件。Multica 会按它自己的存储规则持有这些配置；Relay 不会替 Multica 管理后续密钥生命周期。

若只替换可执行文件路径，可以把同一二进制复制或链接为 `relay-codex` / `relay-claude`（Windows 加 `.exe`）。Relay 根据文件名进入包装模式，`RELAY_PROVIDER` 指定供应商，未设置则用 current。把 Multica 的原生可执行文件路径指向这个实际文件；**不要把带参数的整串命令填进可执行文件路径字段**。

```powershell
Copy-Item .\bin\relay.exe .\bin\relay-codex.exe
# 在 Multica 启动进程的环境中配置 RELAY_PROVIDER=example、RELAY_HOME=对应目录
```

包装模式保留 Codex stdio RPC；不会为 app-server 额外插入 exec，也不会把外层已经回答的问题再次当成退出码 11。

Claude 接入还受其 settings 隔离限制：Multica 写入工作目录的 `CLAUDE.md` 需要显式传入；若 Multica 开启了自行注入 `--settings` 的技能管理策略，会与 Relay 管理的 settings 竞争，当前会明确拒绝该启动。上述端到端包装验证针对 Codex，不能视为所有 Multica Claude 策略组合的兼容保证。

## 存储与安全

默认 `~/.relay/`，通过 `--home` / `RELAY_HOME` 改变。数据库、渲染产物、current、sessions 和实例记录都存于该目录。

- AES-256-GCM 加密每条凭据；供应商 ID/字段名绑定为认证上下文，MCP/prompts 快照也加密。
- 首选系统 keyring 保存随机主密钥。不可用时提示设置至少 12 字符口令，以 Argon2id、随机 salt 与机器身份派生主密钥；无人值守可提供 `RELAY_PASSPHRASE`，该变量不会传给模型 CLI。
- 已建密钥库不会静默换后端。口令、系统 keyring 或机器身份变化可能导致无法解密；本版尚无跨机器密钥迁移命令。备份必须保留对应密钥访问能力。
- 配置产物不含明文 API key。Unix 私有目录/文件为 0700/0600；Windows 为当前用户和 SYSTEM 设置 ACL。
- 导入器限制 SQL 语句能力和大小，拒绝 ATTACH、触发器、虚拟表、文件函数和危险 PRAGMA；不会把原始 SQL 错误内容打印出来。
- 路径限制拒绝供应商/session ID 遍历及符号链接文件。此工具不承诺抵御已经拥有同用户权限、并发篡改所有父目录的本机攻击者。
- 导入未知 target 会保留数据库条目及加密原配置，不生成适配产物。导入报告提示旧 current，不替用户自动切换。

可以通过 `RELAY_CODEX_BIN` / `RELAY_CLAUDE_CODE_BIN` 指定原生可执行文件。Windows 自动识别常见 npm Codex 安装目录中的 `codex.exe`，不会把原生参数交给 `cmd.exe` 二次解释。

## 开发验证

```sh
go test ./...
go vet ./...
```

默认测试使用临时目录、虚构凭据和 helper 子进程，不调用真实模型、不改用户配置。已验证 Windows 原生构建、Linux amd64 / macOS arm64 的 CGO=0 交叉构建。Codex 本机集成需要显式设置 `RELAY_CODEX_INTEGRATION=1`，仅连接测试内的 127.0.0.1 SSE 服务：

```powershell
$env:RELAY_CODEX_INTEGRATION = '1'
go test ./internal/adapter/codex -run Integration -count=1
Remove-Item Env:RELAY_CODEX_INTEGRATION

# 可选：用户明确指定的真实 dump，只在临时数据目录验证，不切换全局配置
python -X utf8 scripts/verify_real_import.py bin/relay.exe path/to/cc-switch-export.sql

# 原生 Codex + Relay CLI + Multica 风格 wrapper，全程仅访问本地假服务
python -X utf8 scripts/verify_codex_relay.py

# 真实 Claude + 本地假服务：确认交接 Skill 与供应商隔离兼容
python -X utf8 scripts/verify_claude_skill.py --relay bin/relay.exe --claude path/to/claude.exe
```

另外在 WSL Ubuntu 20.04 实际执行了 Linux 进程与 CLI 测试，验证 Unix `run` 替换进程后 PID 保持不变且保留原生退出码。新增 GitHub Actions 覆盖 Windows/Linux/macOS 原生测试与安装器验收；macOS 原生验证及 Linux race 检查已通过。

没有对真实 Multica 服务创建 agent，也没有使用真实供应商发出付费请求。集成验证覆盖本地源码接口、原生 Codex 加本地假服务及模拟子进程。
