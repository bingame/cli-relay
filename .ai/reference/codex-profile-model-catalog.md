# 配置 Codex CLI 多 Profile

在 Codex CLI 中为本地 Agent 配置多个独立 Profile，并让每个 Profile 使用自己的模型 catalog、provider 和默认模型。

## 说明多 Profile 的目标和边界

本指南会带你在 Codex CLI 中创建多个本地 Profile，让每个 Profile 通过自己的 `profile-name.config.toml` 选择独立的 `model_catalog_json`、`model_provider`、`model` 和自定义 provider；最后用命令验证目录、JSON、TOML 和 `--profile` 选择是否正确。这里不接入 OpenAI API 或 SDK，也不编写应用代码，只配置本地 Codex CLI 行为。

### Resources
- [Codex CLI](https://developers.openai.com/codex/cli) - Use Codex from your terminal and scripts.
- [Configuration](https://developers.openai.com/codex/configuration) - Configure ChatGPT and Codex developer tools, reusable workflows, and external tools

## 创建 CODEX_HOME 目录结构

把所有机器级配置放在 `CODEX_HOME` 下；未显式设置时通常使用默认的 `~/.codex`，如果要隔离实验环境，可在 shell 中导出 `CODEX_HOME=/path/to/codex-home`。Profile 文件必须与 `config.toml` 放在同一层，并命名为 `$CODEX_HOME/<profile-name>.config.toml`，再用 `--profile <profile-name>` 选择；不要依赖项目内 `.codex/config.toml` 覆盖 provider 或 profile 选择，因为当前配置参考说明项目级配置会忽略这类机器本地 provider/profile 键。

### Code example

```text
$CODEX_HOME/
├── config.toml                       # 可选：全局默认值，不放敏感 token
├── local-a.config.toml               # Profile: local-a
├── local-b.config.toml               # Profile: local-b
└── catalogs/
    ├── local-a.models.json           # 从 Codex bundled catalog 复制后修改
    └── local-b.models.json           # 从 Codex bundled catalog 复制后修改
```

## 为每个 Profile 配置 catalog、provider 和模型

在每个 `profile-name.config.toml` 的顶层放置 `model_catalog_json`、`model_provider` 和 `model`；把 provider 连接信息放在对应的 `[model_providers.<id>]` 表中。关键点是：`model_catalog_json` 是顶层配置项，并且可被当前选中的 Profile 覆盖；不要把它写进 `[model_providers.<id>]`，否则它不是 provider 定义的一部分。自定义 provider ID 不要使用保留的内置 ID，例如 `openai`、`ollama` 或 `lmstudio`。

### Code example

```toml
# $CODEX_HOME/local-a.config.toml
model_catalog_json = "catalogs/local-a.models.json"
model_provider = "local_a_provider"
model = "local-a-model"

[model_providers.local_a_provider]
name = "Local A Provider"
base_url = "https://provider-a.example.com/v1"
env_key = "PROVIDER_A_API_KEY"
wire_api = "responses"

# $CODEX_HOME/local-b.config.toml
model_catalog_json = "catalogs/local-b.models.json"
model_provider = "local_b_provider"
model = "local-b-model"

[model_providers.local_b_provider]
name = "Local B Provider"
base_url = "https://provider-b.example.com/v1"
env_key = "PROVIDER_B_API_KEY"
wire_api = "responses"
```

## 准备模型 catalog 文件

为每个 Profile 准备自己的 `catalogs/*.models.json`，但不要凭空编写未公开的 `models.json` schema。推荐从与你当前 Codex CLI 版本一致的 bundled model catalog 复制一份作为模板，只修改需要的模型条目、模型 ID 和能力描述，并保留模板中的整体结构；如果升级 Codex CLI，重新以新版本 bundled catalog 为基准合并本地改动。`model` 的值应当能在该 Profile 指向的 catalog 中找到，并且与 `model_provider` 指向的 provider 兼容。

目前公开文档只说明 `model_catalog_json` 是“启动时加载的 JSON 模型 catalog”，没有公开完整的 `models.json` schema。最稳妥的做法是先导出 Codex 自带 catalog，再修改：

```bash
codex debug models --bundled > ~/.codex/models.json
```

然后在 `~/.codex/config.toml` 中指定：

```toml
model_catalog_json = "~/.codex/models.json"
```

编辑时建议只修改现有模型条目的显示名称、模型 ID 或 provider 映射，保留原有字段结构。检查是否加载成功：

```bash
codex debug models
```

相关文档：

- [Configuration Reference](https://learn.chatgpt.com/docs/config-file/config-reference)
    
- [Developer commands —`codex debug models`](https://learn.chatgpt.com/docs/developer-commands#codex-debug-models)
    

注意：不要直接使用自定义的简单数组格式；应以 `codex debug models --bundled` 输出的完整结构为模板。



## 用命令选择 Profile 并验证配置

先检查 JSON 与 TOML 的基本语法，再用 `--profile` 分别启动 Codex，确认所选 Profile 的 `model_catalog_json` 覆盖生效。下面的命令只验证文件位置、语法和启动路径；真实 provider 请求还需要设置对应的 API key 环境变量，并确保 `base_url` 支持 Codex 当前文档中 `wire_api = "responses"` 的协议约定。

### Code example

```bash
export CODEX_HOME="$HOME/.codex"

# 1) JSON 语法检查：不验证未公开 schema，只确认文件是合法 JSON。
jq empty "$CODEX_HOME/catalogs/local-a.models.json"
jq empty "$CODEX_HOME/catalogs/local-b.models.json"

# 2) 快速检查 model_catalog_json 没有被误放到 provider 表中。
grep -n '^model_catalog_json' "$CODEX_HOME"/*.config.toml
grep -n '^\[model_providers\.' "$CODEX_HOME"/*.config.toml

# 3) 设置各 provider 的凭据环境变量。
export PROVIDER_A_API_KEY="<provider-a-api-key>"
export PROVIDER_B_API_KEY="<provider-b-api-key>"

# 4) 分别选择 Profile 启动，确认当前 Profile 可加载并使用自己的模型配置。
codex --profile local-a "Say which profile/model configuration is active."
codex --profile local-b "Say which profile/model configuration is active."
```
