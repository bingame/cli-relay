# 实现与验收记录

日期：2026-09-17；最近更新：2026-09-20。依据 `relay-spec.md`，使用 Go / Cobra / modernc SQLite / go-keyring。文档使用中文；未修改 cc-switch、Multica 仓库，也未自动切换用户的真实原生配置。

## 2026-09-20：Spec v0.7 增量

- 对照 cc-switch 源码提交 `06082e189d65e6d6dbadc35dacdac1ce6c79d89a` 的 `src-tauri/src/database/schema.rs`、`database/backup.rs`，确认 SQL 导出包含现场 schema，且导出器本身通过 `PRAGMA table_info` 取得实际列。
- cc-switch 导入器改为先对临时库执行 `PRAGMA table_info(providers)`，保留列名、声明类型、非空、默认值和主键信息，再建立当前源码确认的语义映射；读取数据时根据映射生成显式列查询，不再使用 `SELECT *` 或依赖列顺序。
- 缺少必需语义字段时安全失败，诊断只包含语义字段名、`user_version` 和实际列名，不包含配置值。未知列继续兼容，`meta` 缺失时不再阻断导入。
- 新增现场 schema 列重排、未知列、可选 `meta`、显式查询引用和缺失字段诊断测试。
- 验收命令：`go test ./...`、`go vet ./...`、`go test ./internal/importer/ccswitch -count=10`、`git diff --check`，退出码均为 0；额外源码检查确认 `internal/importer/ccswitch` 中没有 `SELECT *`。

## 安装与分发补充

- 增加 GoReleaser v2.9.0 配置、三系统 CI 和 tag 发布工作流；五平台 CGO=0 归档、Windows 裸 exe、SHA-256、Homebrew formula、Scoop manifest 由同一配置生成。版本固定原因见 `distribution/NOTES.md`。
- 一行安装器支持公开 HTTPS 和现有私有仓库的 `RELAY_DOWNLOAD_MODE=gh`；先校验再原子替换，PATH 配置、升级、自动 Skill 安装都有明确失败行为。用户已确认先使用现有 `bingame/cli-relay`，不要求新域名上线。
- `InstallSkill(targetDir string) error` 加入两个 Adapter；`relay skill install [--cli claude-code,codex]` 自动探测、离线安装，支持原生配置目录覆盖、幂等升级和用户修改保护，不初始化数据库或凭据库。
- 修复 Claude 隔离设置关闭用户 Skill 发现的问题：只额外加载 Relay 内嵌 Handoff 插件，保留供应商隔离。真实 Claude + 本地假服务验证 Skill 可见且使用正确的虚构凭据；复现入口 `scripts/verify_claude_skill.py`。
- Go 模块路径改为 `github.com/bingame/cli-relay`，支持正常模块安装；增加发布版本输出。更新规范、README、Adapter NOTES 与发布说明。

本地验收：Windows `go test ./...` / `go vet ./...`，GoReleaser `check` / 五平台 snapshot，发行产物及软件源校验和核对，Windows PowerShell 5.1/7 安装器回归，WSL Linux 安装器/CLI/Skill 测试与 ShellCheck，Skill 格式校验，工作流 Actionlint 检查。Linux amd64 产物确认为静态链接。

GitHub Actions 首轮已通过 Linux（含 race）、macOS 原生测试及五平台打包检查。Windows runner 发现历史 SQL fixture 被 Git 的 autocrlf 改写，导致多行原文保真断言失败；已为 `.sql` 固定 LF，保持测试原文与版本库字节一致，未放宽导入器的保真要求。

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
- 2026-09-20 用用户 2026-09-19 生成的新导出（14 条供应商、18 列）复验：`TestParseRealDumpStatistics`、`TestRealDumpAdapterIntegration`（期望值改为从 dump 现场推导，不再硬编码上次的 15）；CLI 端到端（隔离 RELAY_HOME）覆盖 dry-run 报告 schema、manual 同名不参与匹配、撞号改名、`--on-conflict skip`、空导出 `--prune` 后 disabled 记录 `switch` 报"已被 cc-switch 同步标记为失效"、manual 记录不受清理影响；数据目录扫描未发现明文凭据。
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
