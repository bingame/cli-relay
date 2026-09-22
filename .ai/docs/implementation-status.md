# 实现与验收记录

日期：2026-09-17；最近更新：2026-09-22。依据 `relay-spec.md`，使用 Go / Cobra / modernc SQLite / go-keyring。文档使用中文；未修改 cc-switch、Multica 仓库，也未自动切换用户的真实原生配置。

## 2026-09-22：模型目录只承载真实声明，两侧对齐（Spec v0.10）

- 现象（用户反馈）：cc-switch 里「模型映射」留空的渠道，经 Relay 启动后模型仍被映射/锁死，例：Codex 的 `axonhub-any`。
- 根因三处（都是把「默认模型」当成「模型目录」）：导入器在无 `modelCatalog` 时用 `Provider.Model` 伪造一条 `provider_models`；CodexAdapter 渲染条件是 `len(models) > 0 || p.Model != ""`；ClaudeCodeAdapter 无条件写 `modelPicker`（`replaceBuiltInOptions: true` 等于替换内置菜单）。
- 修正：`provider_models` 只承载源里真实声明的模型目录，为空就不渲染任何目录类字段——导入器删除伪造分支、按 `settings_config.modelCatalog` 逐条导入；CodexAdapter 改为 `if len(models) > 0` 才写 `model_catalog_json`（不写时 Codex 自己拉 `/v1/models`，与 cc-switch「模型映射留空就不生成 catalog」一致）；ClaudeCodeAdapter 同样加 gating，模型选择交回原生菜单与 `ANTHROPIC_DEFAULT_*` 环境变量。`relay provider add --model X` 仍写入一条记录，属于用户显式声明，是刻意保留的非对称。
- 新增 `provider_models` 列 `reasoning_levels`、`default_reasoning_level`（含 `ALTER TABLE` 迁移），对齐 cc-switch「模型映射」里用户可编辑的思考档位：渲染时按 Codex canonical 顺序（`none<minimal<low<medium<high<xhigh<max<ultra`）替换条目的支持档位、丢弃未知值，默认档走「声明 → 模板默认 → 最高」三级回落。spec §5.2.1 首次写明「可编辑字段边界」（Codex：显示名/请求模型/上下文窗口/思考档位；Claude Code：显示名/请求模型/1M 上下文/默认兜底模型），其余字段沿用 CLI 原生默认值，Relay 不提供编辑面。
- 测试：`TestImportDoesNotInventModelCatalog`、`TestCodexModelCatalogImport`（trim/去重/逐列还原）、`TestCatalogSkippedWithoutDeclaredModels`、`TestCatalogHonorsPerModelReasoningLevels`、`TestOpenMigratesLegacyModelColumns`、`TestStoreRoundTripsModelReasoningLevels`。
- 测试卫生修复：`TestNativeConfigMappingAndCredentialRejection` 与 `checkAdapterIntegration` 此前会写用户真实 `$CODEX_HOME`（安装 profile + 管理块），现均隔离到 `t.TempDir()`。修复前后用 `ls ~/.codex/*.config.toml ~/.codex/config.toml ~/.claude/settings.json | md5sum` 比对确认为同一哈希，`go test ./...` 不再触碰真实 HOME。
- 存量数据：Relay 库中已导入的记录若模型目录是被伪造出来的，需重新导入才会消失（重导会按真实 `modelCatalog` 重写记录与产物）。
- 验收：`go test ./...`、`go build ./...`、`go vet ./...` 全过；真实导出（用户 dump，只读）经 `TestRealDumpAdapterIntegration` 验证 10 条供应商及冲突改名版本，产物与 argv 无明文凭据。

## 2026-09-22：Codex 模型目录（`model_catalog_json`）按 Codex 原生 schema 重写（Spec v0.9）

- 现象：`relay run codex --provider axonhub-codex` 在 codex-cli 0.155.1 上 `Error loading configuration: failed to parse model_catalog_json path ...: missing field `slug``。根因是渲染的 catalog 用了 Relay 自拟结构（只有模型 ID），而该文件是 Codex 的完整模型定义目录。
- 实测确定契约：必需字段 `slug`/`display_name`/`supported_reasoning_levels`/`shell_type`/`visibility`/`supported_in_api`/`priority`/`support_verbosity`/`truncation_policy`/`experimental_supported_tools` + `base_instructions`（或 `model_messages.instructions_template`）；条目是 slug 的替代、无内置继承；设置该文件后 Codex 不再拉取 `/v1/models`；供应商网关实测 `GET /v1/models` 返回 200（26 个模型，仅一次 GET，未打印密钥）。
- 新增 `internal/adapter/codex/catalog.go`：内置中性模板（形态与 cc-switch 为非官方模型生成的 `cc-switch-model-catalog.json` 一致，`shell_type = "shell_command"`，不声明 `apply_patch_tool_type`/`web_search_tool_type`/`tools`/`model_messages`，避免第三方 `/responses` 网关拒绝 freeform `apply_patch`），逐模型克隆并覆写 `slug`/`display_name`/`description`/`context_window`/`max_context_window`/`priority`（`1000 + 序号`）；供应商默认模型始终入目录；profile 声明的 `model_reasoning_effort` 并入该条目的支持档位；渲染前逐条自检必需字段，无可渲染模型时不写 catalog 也不设该配置项。
- 测试：`internal/adapter/codex/catalog_test.go` 覆盖原生字段形态、不含 freeform 工具声明、默认模型必在目录内、上下文窗口优先级（模型记录 > `codex_config` > 128000）、推理档位补齐与未知档位忽略、条目独立性、重复/空模型整理、无可用模型时不写 catalog、缺字段拒绝渲染。
- 端到端复核：用真实库中的 `axonhub-codex` 记录跑 `Render` + `BuildLaunchInputs`，产出的 profile/catalog 交给本机 `codex.exe`（仅把 `base_url` 指向本地假服务、`auth.command` 换成占位环境变量，未请求真实网关、未使用真实凭据）：退出 0、`turn.completed`、`model=gpt-6-astra`、`reasoning.effort=low`、instructions 117 字符、工具集无 `apply_patch`。
- 本次未做的：`/v1/models` 拉取与模型同步（用户明确选择"只修 catalog"）。当时的现状是 `provider_models` 里有什么就渲染什么，未导入模型列表的供应商只渲染 `provider.Model` 一条——**该行为已在同日 v0.10 修正**：未声明模型目录的供应商不再渲染目录（见上一节）。
- 验收：`go test ./...`、`go vet ./...` 全过。

## 2026-09-21：Spec v0.8 增量（导入白名单落地 + 供应商名称解析与补全）

- 落地 v0.7 §6 第 4 步白名单：导入侧 `extra.claude_settings` 只保留 `env`、`extra.codex_config` 只保留 Adapter 消费的字段（本地维护与 adapter/codex 的 `providerFields`/`profileFields` 一致的键集，注释互相指向）；渲染侧 ClaudeCodeAdapter 不再以整份 `claude_settings` 为基底，只取 `env`。permissions/hooks/statusLine/sandbox_mode 等不再入库、不进渲染产物，原始内容仍在加密的 `source_settings` 快照。
- 存量数据边界：白名单只影响新导入；v0.6 之前以 cc-switch UUID 为 ID 的旧库按用户决定清库重导，不提供迁移命令（spec v0.8 已记录该边界）。
- 新增 `internal/cli/resolve.go`：所有接受供应商的参数（`--provider`、`switch`、`remove`、`render-args/render-env`、`secret get`，含 handoff 的 `--provider`/`--summary-provider`）同时接受 ID 或显示名称，ID 精确优先、名称重名报错列候选；并为这些参数注册 cobra 动态补全（ID 与无空白显示名称互为候选，失败静默 `NoFileComp`）。
- 补全函数防御：未执行命令上 `cmd.Context()` 为 nil 会让 `database/sql` panic 并在 defer Close 死锁，补全路径显式回退 `context.Background()`（单测直接调用补全函数复现后修复）。
- 测试：导入白名单（hooks/permissions 剔除、env 保留、白名单字段内凭据副本仍剥离）、Codex 执行环境字段剔除、Adapter 渲染白名单消费、ApplyGlobal 用户修改旧字段保留（改用 env 字段表达，原依赖 permissions 透传）、名称解析（ID/名称/重名/未找到/激活保护）、补全候选与 target 过滤。
- 验收：`go test ./...`、`go vet ./...`、`git diff --check` 全过；隔离 RELAY_HOME 端到端：`provider import` 得友好 slug ID，`provider list` 输出仅含白名单字段，`switch`/`render-args` 按显示名称工作，未找到时给出可操作建议。

## 2026-09-20：Spec v0.7 增量

- 对照 cc-switch 源码提交 `06082e189d65e6d6dbadc35dacdac1ce6c79d89a` 的 `src-tauri/src/database/schema.rs`、`database/backup.rs`，确认 SQL 导出包含现场 schema，且导出器本身通过 `PRAGMA table_info` 取得实际列。
- cc-switch 导入器改为先对临时库执行 `PRAGMA table_info(providers)`，保留列名、声明类型、非空、默认值和主键信息，再建立当前源码确认的语义映射；读取数据时根据映射生成显式列查询，不再使用 `SELECT *` 或依赖列顺序。
- 缺少必需语义字段时安全失败，诊断只包含语义字段名、`user_version` 和实际列名，不包含配置值。未知列继续兼容，`meta` 缺失时不再阻断导入。
- 新增现场 schema 列重排、未知列、可选 `meta`、显式查询引用和缺失字段诊断测试。
- 验收命令：`go test ./...`、`go vet ./...`、`go test ./internal/importer/ccswitch -count=10`、`git diff --check`，退出码均为 0；额外源码检查确认 `internal/importer/ccswitch` 中没有 `SELECT *`。

## 安装与分发补充

- 增加 GoReleaser v2.9.0 配置、三系统 CI 和 tag 发布工作流；五平台 CGO=0 归档、Windows 裸 exe、SHA-256、Homebrew formula、Scoop manifest 由同一配置生成。版本固定原因见 `distribution/NOTES.md`。
- 一行安装器支持公开 HTTPS 和现有私有仓库的 `RELAY_DOWNLOAD_MODE=gh`；先校验再原子替换，PATH 配置、升级、自动 Skill 安装都有明确失败行为。用户已确认先使用现有 `bingame/cli-relay`，不要求新域名上线。
- `InstallSkill(targetDir string) error` 加入两个 Adapter；`relay skill install [--cli claude,codex]` 自动探测、离线安装，支持原生配置目录覆盖、幂等升级和用户修改保护，不初始化数据库或凭据库。
- 修复 Claude 隔离设置关闭用户 Skill 发现的问题：只额外加载 Relay 内嵌 Handoff 插件，保留供应商隔离。真实 Claude + 本地假服务验证 Skill 可见且使用正确的虚构凭据；复现入口 `scripts/verify_claude_skill.py`。
- Go 模块路径改为 `github.com/bingame/cli-relay`，支持正常模块安装；增加发布版本输出。更新规范、README、Adapter NOTES 与发布说明。

本地验收：Windows `go test ./...` / `go vet ./...`，GoReleaser `check` / 五平台 snapshot，发行产物及软件源校验和核对，Windows PowerShell 5.1/7 安装器回归，WSL Linux 安装器/CLI/Skill 测试与 ShellCheck，Skill 格式校验，工作流 Actionlint 检查。Linux amd64 产物确认为静态链接。

GitHub Actions 首轮已通过 Linux（含 race）、macOS 原生测试及五平台打包检查。Windows runner 发现历史 SQL fixture 被 Git 的 autocrlf 改写，导致多行原文保真断言失败；已为 `.sql` 固定 LF，保持测试原文与版本库字节一致，未放宽导入器的保真要求。

## 阶段

| 阶段 | 交付 |
| --- | --- |
| 前置验证 | Codex 0.154.0 / Claude Code 2.1.268 参数与本地假服务验证，真实 cc-switch schema/导出头验证；各模块 NOTES |
| P0 | Provider SQLite/AES-GCM、两个 Adapter、mock 契约、add/list/remove/switch/run/status、私有文件权限 |
| P1 | SQL 内存导入与快照加密、按 `(target,id)` 直接覆盖、dry-run；受监管执行、退出分类、会话记录、子进程树收尾 |
| P2 | relay-handoff Skill、Markdown/YAML/JSON Schema 验证、活 agent 输出接收、continue 与硬约束重复强调 |
| P3 | Codex 官方 thread/read 优先、无头 resume、容错 JSONL、可选总结供应商、来源凭据脱敏、降级标识 |
| P4 | 仅独立接口预留；不含重试循环、通知发送或关机 |
| P5 | render-args / render-env JSON 对接；二进制别名包装；Codex 的 Multica 风格双向 stdio 完整本地验证 |

## 已运行检查

- Windows：`go test ./...`、`go vet ./...`。
- 真实 SQL 完整 CLI 验证：18 条供应商、15 条支持的 target 渲染，61 条加密秘密数据、1 份加密 MCP/prompts 快照；dry-run 无写入，导入不自动 switch，输出及落盘文件未发现已识别凭据明文。
- 2026-09-20 用用户 2026-09-19 生成的新导出（14 条供应商、18 列）复验：`TestParseRealDumpStatistics`、`TestRealDumpAdapterIntegration`（期望值改为从 dump 现场推导，不再硬编码上次的 15）；CLI 端到端（隔离 RELAY_HOME）覆盖 dry-run 报告 schema、按 `(target,id)` 直接覆盖（含 manual 记录）、`--on-conflict skip`、空导出 `--prune` 后 disabled 记录 `switch` 报"已被 cc-switch 同步标记为失效"、manual 记录不受清理影响；数据目录扫描未发现明文凭据。
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
