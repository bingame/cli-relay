# cc-switch SQL 导入验证记录

验证时间：2026-09-17；2026-09-20 按 Relay spec v0.7 复核。来源为本地 cc-switch 源码和用户指定的真实 SQL 导出；未将导出或其中凭据复制进仓库。

- 2026-09-20 对照的 cc-switch 源码提交为 `06082e189d65e6d6dbadc35dacdac1ce6c79d89a`，工作树无本地修改。当前 `src-tauri/src/database/schema.rs` 中 `providers` 的基础表定义包含 `id`、`app_type`、`name`、`settings_config`、`website_url`、`category`、`created_at`、`sort_index`、`notes`、`icon`、`icon_color`、`meta`、`is_current`、`in_failover_queue`，主键为 `(id, app_type)`；迁移后的真实数据库可能有更多列，因此 Relay 不把这份列表固化为导入契约。
- `src-tauri/src/database/backup.rs` 会从 `sqlite_master` 导出实际建表语句，并通过 `PRAGMA table_info` 取得当时数据库的实际列名来生成带列名的 INSERT。Relay 因此在临时库重建完成后同样执行 `PRAGMA table_info`，读取列名、声明类型、非空、默认值和主键信息，再解析当前源码已确认的语义映射。

- `src-tauri/src/database/backup.rs` 中常量 `CC_SWITCH_SQL_EXPORT_HEADER` 的**值**是 `-- CC Switch SQLite 导出`。导入校验这个实际前缀，不校验常量名称。
- 实际 dump 的 user_version 为 18；通过 SQLite 在内存执行后查询 PRAGMA table_info 得到：`id, app_type, name, settings_config, website_url, category, created_at, sort_index, notes, icon, icon_color, meta, is_current, in_failover_queue, cost_multiplier, limit_daily_usd, limit_monthly_usd, provider_type`。
- 主键是 `(id, app_type)`；Relay 的单列 ID 需要处理同一 dump 内和已存数据的冲突。
- 共 18 条 provider：claude 8、codex 7、claude-desktop 1、gemini 1、grokbuild 1；mcp_servers 1 条、prompts 2 条。
- Claude 配置在 settings_config JSON 的 env 内，常见 `ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_MODEL`；也可能有顶层 model 和 permissions/hooks 等原生字段。
- Codex 配置是 `{auth: {OPENAI_API_KEY: ...}, config: "TOML 文本"}`；必须解析 TOML 并按 model_provider 查找对应 model_providers 节，不能假定字段在 JSON 顶层。
- 其他 target 保留记录及加密原始配置，不生成适配器产物，不静默丢弃。
- SQL 在内存 SQLite 执行，禁止 ATTACH/文件写入类操作；错误不得带出可能含密钥的 SQL。原始配置、meta、MCP/prompts 快照须加密。

## 实现与安全边界

- 使用 `modernc.org/sqlite v1.59.0`。其 `Driver.Open` 文档明确说明目前没有公开 authorizer，因此先运行真正区分 SQL 单词、字符串、引用标识符及注释的词法检查，再把完整脚本交给 SQLite 执行；绝不自行解析 INSERT 数据。
- 每次导入创建独立 `sqlite.Driver` 和私有内存数据库，不继承全局注册的自定义函数或虚拟表。启用 defensive、关闭 trusted_schema、临时存储限定内存；禁用附加数据库，并限制输入为 64 MiB、数据库页数、表达式深度、单表读取行数与 20 秒执行时间。
- 默认只放行建表/索引/视图、INSERT/DELETE 和事务语句；PRAGMA 仅允许 dump 实际使用的 `foreign_keys` 与 `user_version`。拒绝 ATTACH、DETACH、VACUUM、扩展加载、文件函数、虚拟表和所有触发器。保守拒绝触发器是当前驱动缺少 authorizer 的明确兼容限制，不影响已验证的真实导出。
- SQL 执行完成后尝试开启新事务，确认原导出已回到 autocommit，拒绝缺少 COMMIT 或 RELEASE 的截断文件。所有 SQLite/TOML/JSON 错误对外只给出阶段说明，不输出原 SQL、配置片段或凭据。
- `providers` 先现场探测 schema，再按探测到的真实拼写生成显式列查询；不使用 `SELECT *`，不依赖物理列顺序。未知列兼容；缺失必需语义字段时只显示 `user_version` 和实际列名，不显示任何行值。`meta` 不是建立 Relay provider 所必需的语义字段，存在时作为加密源数据保留。ID 保持大小写；非法 ID 规范为安全 slug 并追加确定性短哈希，原 ID 保留在解析结果供导入报告使用。ID 与本地冲突由调用方按 `(target, id)` 直接覆盖处理（不再生成数字后缀改名），不在此处改名。
- Claude 原生配置中凭据从 `env` 剥离到 `Secrets["env:变量名"]`；主凭据优先 `ANTHROPIC_AUTH_TOKEN`，其次 `ANTHROPIC_API_KEY`。使用 `Secrets["api_key_env"]` 保留所选原生鉴权变量名，避免把 API key 错当 bearer token；两种凭据同时存在时只注入优先的一种，另一种仍保存在加密源配置中。普通 settings 字段继续透传，数值型 token 预算不会被误当凭据删除。
- Codex 主凭据优先 `auth.OPENAI_API_KEY`，其次已选 provider 的 bearer/api_key，再其次 JSON env 中原 `env_key` 所引用的值。内联 `http_headers` 转为确定性环境变量引用，值进入 Secrets；已有 `env_http_headers` 映射保留，包括 `Authorization` header 名。原始配置和 meta 无论 target 是否受支持都以敏感数据返回，由调用方加密保存。
- MCP/prompts 快照包含列名与原始行，字符串、NULL、数值和 BLOB 均由 SQLite 读取后编码为 JSON；Snapshot 不得写入明文日志或文件。
- 提取后执行第二轮已知凭据检查：公开字段含已知凭据时拒绝记录；Extra 普通字符串或字段名复制凭据时删除该字段，含凭据的 hook command 则删除整个 hook 对象。解析结果 `Warnings` 仅报告字段路径，不包含原值，完整原配置仍通过加密源数据保留。主 API key 无论长度都检查；其他环境/字段凭据至少 8 字节才参与复制检测，避免把 `true` 等短配置值误作泄露。

## 验证结果

- `go test ./internal/importer/ccswitch -count=1` 覆盖 PRAGMA schema 探测、显式列查询、列重排/未知列/可选 meta、多行批量 INSERT、SQL 转义和嵌入式伪 SQL、两类 CLI 字段与密钥拆分、不支持 target、事务截断、损坏 JSON/TOML、缺失语义字段诊断、大小限制和恶意 SQL 文件副作用。
- 2026-09-20 增量验收：`go test ./...`、`go vet ./...`、`go test ./internal/importer/ccswitch -count=10`、`git diff --check` 均通过；源码检查确认 `internal/importer/ccswitch` 不再包含 `SELECT *`。
- 使用环境变量 `RELAY_TEST_CCSWITCH_DUMP` 显式指定用户原导出，运行 `TestParseRealDumpStatistics`；测试只输出计数且不复制源数据。真实结果为 18 条供应商、18 列，目标分布与上述探测一致；已提取的明文凭据未出现在 Provider 元数据中。
- 2026-09-20 复验用户 2026-09-19 生成的新导出（14 条供应商、18 列）：目标分布 claude 6、codex 6、claude-desktop 1、grokbuild 1。`TestRealDumpAdapterIntegration` 的期望值改为从 dump 现场推导（上次导出的 15 属于用户数据快照，不应固化为契约），本次验证 12 条受支持供应商均通过。
- `TestRealDumpAdapterIntegration` 对真实导出的全部 15 条受支持供应商执行 Render/BuildLaunchInputs，产物仅写测试临时目录，验证配置文件/argv 无明文凭据且环境变量引用有效；不调用真实 CLI 或修改全局原生配置。

## 模型目录导入（2026-09-22）

- 现象：cc-switch 里「模型映射」留空的渠道，经 Relay 导入后仍被映射/锁定成一个模型。
- 现场核对（本地 cc-switch 工作区和用户导出）：Codex 的模型映射**只**存在 `settings_config.modelCatalog = { models: [...] }` 里，且 cc-switch 仅在非空时生成 `cc-switch-model-catalog.json` 并设置 `model_catalog_json`；模型映射为空的渠道不会生成目录条目。
- 根因（本仓库侧）：导入器在 `entry.Models` 为空时用 `Provider.Model` **伪造**一条 `provider_models`，Adapter 又据此写出 `model_catalog_json`。现已删除该伪造分支，改为 `entry.Models = append(entry.Models, codexCatalogModels(settings["modelCatalog"], entry.Provider.Model)...)`——**源里没声明就不产生任何模型记录**。
- `codexCatalogModels` 逐条读取 cc-switch 「模型映射」表里用户可编辑的四列：`model`（trim 后作 model_id，空则跳过、重复则去重）、`displayName`、`contextWindow`、`reasoningLevels` + `defaultReasoningLevel`；`is_default` 来自 `modelID == 源里的默认模型`，`sortOrder` 按出现顺序。`catalogReasoningLevels` 只做 trim/去重（未知档位留给 Adapter 按 Codex canonical 列表过滤，与 cc-switch「声明 ∩ canonical」等价），`catalogInt` 兼容 JSON 里的 float64/int64/json.Number/字符串，非正整数一律忽略。
- 回归测试：`TestCodexModelCatalogImport`（有 `modelCatalog` 时逐列还原，含 trim 与去重）、`TestImportDoesNotInventModelCatalog`（无 `modelCatalog` 时 `entry.Models` 必须为空且不因 `Provider.Model` 非空而出现记录）。


## 2026-09-24：slug 歧义拒绝与 skip 语义对齐

- **slug 歧义拒绝**：`Slugify(display_name)` 不保证全局唯一——同一 CLI 下 "A B"/"A-B" 会归一化为同一 ID `a-b`。批内或库内已存在同一 `(target, id)` 但显示名称不同时，导入器明确拒绝写入（报告字段 `rejected: true` + `warnings`），不追加数字后缀，避免误合并。
- **skip 匹配键对齐**：`--on-conflict skip` 的匹配键与存储层 `Upsert` 的"按 id 或 display_name 删除"保持一致：`(target, id)` 或 `(target, display_name)` 任一命中即视为冲突，避免同名不同 ID 的旧记录被静默替换。
