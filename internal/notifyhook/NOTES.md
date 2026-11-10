# notifyhook NOTES（relay notify 上游依据与差异）

记录各 CLI 通知机制的依据来源与已验证/待验证状态。实现前（2026-10-26）逐一核实，不假设。

## Claude Code（~/.claude/settings.json，用户级）

- **已验证（官方 hooks 文档 + 本机既有配置）**：
  - hooks 结构为 `hooks.<Event>[] -> { matcher?, hooks: [{ type:"command", command, timeout }] }`，`timeout` 单位秒；事件与 matcher 均支持（Stop 无 matcher，PreToolUse matcher 为正则）。
  - `Stop` 在主 agent 结束一轮响应时触发；`Notification` 在权限请求/等待输入时触发；`PreToolUse(AskUserQuestion|ExitPlanMode)` 覆盖"提问/计划待审"两类人工介入（AskUserQuestion 是否总是触发 Notification 未获官方保证，故显式挂 matcher）。
  - **用户级 hooks 不触发会话内审批**；只有项目级 `.claude/settings.json` 的 hooks 需要 "Approve new hooks" 确认——这是本设计全部走用户级的原因。
  - 载荷字段（stdin JSON）：`hook_event_name`、`session_id`、`cwd`、`message`（Notification）、`stop_hook_active`（Stop 续跑标志）、`tool_name`/`tool_input`（PreToolUse）。
- **待实测**：真实会话中三条 hook 的实际触发时序（尤其 Windows 下 `"...relay.exe" notify hook claude` 双引号+正斜杠命令串的 shell 解释）。命令串样式沿用本机 `.orca` 条目先例（正斜杠+引号）。
- 与 `ApplyGlobal` 的互操作已测：switch 只动自己托管字段，hooks 条目原样保留；反向亦然（`TestClaudeHooksSurviveSwitch`）。

## Codex（~/.codex/config.toml，顶层 `notify`）

- **已验证（官方配置文档 + 多方社区一致）**：
  - `notify = ["<program>", "<固定参数>..."]`；Codex 把事件 JSON 作为**最后一个 argv 参数**追加调用，不用 stdin。事件：`agent-turn-complete`（含 `thread_id`、`last_assistant_message` 等）、`approval-requested`。
  - 一个 config 只支持一个 `notify` 值——本机该键默认被 Codex 桌面端（`codex-computer-use.exe`）占用，因此实现**链式接管**：旧值存 `notify-state.json`，relay hook 先推送再以"旧程序+旧参数+载荷"转发（3s 上限，失败仅记日志），卸载还原，用户手改过的值不覆盖。
- **待实测**：真实 Codex 会话的 argv 载荷字段全集（当前按公开文档子集解析，未知字段自然忽略）；Windows 下 notify 程序的 stdout 处理（当前主动丢弃）。

## ZCode（~/.zcode/cli/config.json，用户级）

- **已验证（ZCode 官方 skill 文档：diagnosing-hooks / zcode-configuration-guide）**：
  - 配置级 hooks 形如 `{ enabled?, timeoutMs?, events: { <Event>: [ { matcher?, hooks: [...] } ] } }`，**默认禁用**，必须 `hooks.enabled: true`；用户/工作区配置 hooks **无信任门槛**（不同于插件审批的说法已过时）。
  - 恰好 7 个事件：`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PermissionRequest`、`PostToolUse`、`PostToolUseFailure`、`Stop`；**不支持** `Notification`/`SubagentStop`。
  - `type:"process"` hook 用 `command`+`args[]` 参数向量（无 shell，Windows 免引号问题），`timeoutMs` 单位毫秒；matcher 为区分大小写正则，对 `PreToolUse` 匹配工具名；alias `Write/Edit ← ApplyPatch`、`Task ↔ Agent`（不影响本设计）。
  - 超时解析顺序 `timeoutMs → timeout×1000 → 配置级 timeoutMs → 60000ms`。
- **待实测**：stdin 载荷字段是否与 Claude Code 同构（实现按 `hook_event_name`/`session_id`/`cwd`/`tool_name`/`tool_input` 解析，缺失字段自然降级：cwd 回退进程工作目录）；`PermissionRequest` 是否也覆盖 AskUserQuestion 场景（不覆盖也无碍，PreToolUse matcher 已显式覆盖）。
- 副作用记录：`enabled: true` 会激活该文件里所有配置级 hooks（安装输出已提示；卸载按 `notify-state.json` 记录还原原值）。

## 通道适配器（internal/plugins）

- Bark：JSON POST 到 `<server>/push`，`device_key`/`title`/`body`/`group` 分字段（title/body 不合并——cli-hub 时代踩过并验证过的约定）；默认服务器 `https://api.day.app`。
- Telegram：`<server>/bot<token>/sendMessage`，`chat_id`+`text`（标题与正文以空行拼接）；默认 `https://api.telegram.org`。
- ntfy：JSON 发布模式 POST 根路径 `{topic,title,message,tags}`，可选 `Authorization: Bearer`；默认 `https://ntfy.sh`。
- webhook：POST 事件 JSON（type+title/body/cli/host/project/session），可选 Bearer。企业微信/钉钉/飞书群机器人可直接用其 webhook 兼容形态（未逐一适配各家加签格式）。

## 本机冒烟（2026-10-26，Windows）

隔离目录真实二进制：`set`（通道文件无令牌明文）→ `install`（三家写入/格式见上）→ `status` → `hook`（无 stdout、退出 0；Bark 假 key 真实请求返回 400，仅记 `notify.log`）→ `uninstall`（三个原生配置文件恢复原状 `{}`/空）。全程未触碰真实 `~/.claude`、`~/.codex`、`~/.zcode`。
