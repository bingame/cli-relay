package codex

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/provider"
)

// Codex 的 model_catalog_json 不是"模型 ID 列表"，而是一份完整的模型定义目录：
// 每条至少要带 slug、display_name、supported_reasoning_levels、shell_type、
// visibility、supported_in_api、priority、support_verbosity、truncation_policy、
// experimental_supported_tools，并且必须给出 base_instructions 或
// model_messages.instructions_template，否则 Codex 直接拒绝加载配置
// （0.155.1 实测报 "missing field `slug`" 或 "missing field `base_instructions`"）。
//
// 第三方 /responses 网关会拒绝 Codex 的 freeform apply_patch（type=="custom"）
// 工具，所以模板刻意不声明 apply_patch_tool_type / web_search_tool_type / tools /
// model_messages，改由 shell_type = "shell_command" 走命令式改动。
// 这与 cc-switch 的 codex_native_responses_template 保持一致（见 NOTES.md）。
const neutralCatalogTemplate = `{
  "slug": "relay-template",
  "display_name": "relay-template",
  "description": "relay-template",
  "base_instructions": "You are Codex, a coding agent. You and the user share the same workspace and collaborate to achieve the user's goals.",
  "default_reasoning_level": "high",
  "supported_reasoning_levels": [
    { "effort": "none", "description": "Disable Thinking" },
    { "effort": "high", "description": "Enabled Thinking" }
  ],
  "shell_type": "shell_command",
  "visibility": "list",
  "supported_in_api": true,
  "supports_reasoning_summaries": true,
  "default_reasoning_summary": "none",
  "support_verbosity": false,
  "truncation_policy": { "mode": "bytes", "limit": 10000 },
  "supports_parallel_tool_calls": false,
  "supports_image_detail_original": false,
  "effective_context_window_percent": 95,
  "experimental_supported_tools": [],
  "input_modalities": ["text", "image"],
  "supports_search_tool": false
}`

// defaultCatalogContextWindow 是模型记录与原生配置都没有声明上下文窗口时的兜底值。
const defaultCatalogContextWindow = 128000

// catalogPriorityBase 让 Relay 生成条目的优先级明显低于 Codex 内置模型，
// 避免遮挡官方条目（与 cc-switch 的 1000 + 序号一致）。
const catalogPriorityBase = 1000

// reasoningEfforts 是 Codex 认识的推理档位及其官方描述，顺序即从低到高。
var reasoningEfforts = []struct {
	effort      string
	description string
}{
	{"none", "Disable Thinking"},
	{"minimal", "Minimal reasoning"},
	{"low", "Fast responses with lighter reasoning"},
	{"medium", "Balances speed and reasoning depth for everyday tasks"},
	{"high", "Greater reasoning depth for complex problems"},
	{"xhigh", "Extra high reasoning depth for complex problems"},
	{"max", "Maximum reasoning depth for the hardest problems"},
	{"ultra", "Ultra reasoning depth"},
}

// catalogRequiredFields 是加载期硬性要求的字段；渲染后逐条自检，绝不写出 Codex 会拒绝的目录。
var catalogRequiredFields = []string{
	"slug", "display_name", "supported_reasoning_levels", "shell_type",
	"visibility", "supported_in_api", "priority", "support_verbosity",
	"truncation_policy", "experimental_supported_tools",
}

func reasoningDescription(effort string) (string, bool) {
	for _, level := range reasoningEfforts {
		if level.effort == effort {
			return level.description, true
		}
	}
	return "", false
}

// parseTemplate 每次返回一份独立副本，条目之间不会共享嵌套结构。
func parseTemplate() (map[string]any, error) {
	var template map[string]any
	if err := json.Unmarshal([]byte(neutralCatalogTemplate), &template); err != nil {
		return nil, fmt.Errorf("Codex 模型目录模板无效")
	}
	return template, nil
}

func cloneTemplate(template map[string]any) (map[string]any, error) {
	data, err := json.Marshal(template)
	if err != nil {
		return nil, fmt.Errorf("Codex 模型目录模板无效")
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("Codex 模型目录模板无效")
	}
	return entry, nil
}

// positiveInt 兼容 SQLite JSON 列往返后的 float64 与原生整数。
func positiveInt(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), number > 0
	case int64:
		return number, number > 0
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil && parsed > 0
	case float64:
		if number > 0 && number == float64(int64(number)) {
			return int64(number), true
		}
	}
	return 0, false
}

// declaredContextWindow 取供应商原生配置里的 model_context_window；它同时会被写进
// profile，目录里的窗口必须与之一致。
func declaredContextWindow(p provider.Provider) (int64, bool) {
	config, ok := p.Extra["codex_config"].(map[string]any)
	if !ok {
		return 0, false
	}
	return positiveInt(config["model_context_window"])
}

func declaredReasoningEffort(p provider.Provider) string {
	config, ok := p.Extra["codex_config"].(map[string]any)
	if !ok {
		return ""
	}
	effort, _ := config["model_reasoning_effort"].(string)
	if _, ok := reasoningDescription(effort); !ok {
		return ""
	}
	return effort
}

// catalogModels 去重并按 sort_order 稳定排序，最后保证供应商默认模型一定在目录里
// （profile 的 model 指向目录外的值会被 Codex 视为未知模型）。
func catalogModels(p provider.Provider, models []provider.Model) []provider.Model {
	seen := map[string]bool{}
	result := make([]provider.Model, 0, len(models)+1)
	for _, model := range models {
		id := strings.TrimSpace(model.ModelID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		model.ModelID = id
		result = append(result, model)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].SortOrder != result[j].SortOrder {
			return result[i].SortOrder < result[j].SortOrder
		}
		return result[i].ModelID < result[j].ModelID
	})
	if p.Model != "" && !seen[p.Model] {
		result = append([]provider.Model{{ProviderID: p.ID, ModelID: p.Model, IsDefault: true}}, result...)
	}
	return result
}

// buildCatalog 把 Relay 的 provider_models 渲染成 Codex 原生目录。
func buildCatalog(p provider.Provider, models []provider.Model) (map[string]any, error) {
	entries := catalogModels(p, models)
	if len(entries) == 0 {
		return nil, nil
	}
	template, err := parseTemplate()
	if err != nil {
		return nil, err
	}
	fallbackWindow, hasFallback := declaredContextWindow(p)
	if !hasFallback {
		fallbackWindow = defaultCatalogContextWindow
	}
	effort := declaredReasoningEffort(p)
	catalog := make([]map[string]any, 0, len(entries))
	for index, model := range entries {
		entry, err := catalogEntry(template, model, index, fallbackWindow, effort)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, entry)
	}
	return map[string]any{"models": catalog}, nil
}

func catalogEntry(template map[string]any, model provider.Model, index int, fallbackWindow int64, effort string) (map[string]any, error) {
	entry, err := cloneTemplate(template)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(model.DisplayName)
	if displayName == "" {
		displayName = model.ModelID
	}
	window := fallbackWindow
	if model.ContextWindow > 0 {
		window = model.ContextWindow
	}
	entry["slug"] = model.ModelID
	entry["display_name"] = displayName
	entry["description"] = displayName
	entry["context_window"] = window
	entry["max_context_window"] = window
	entry["priority"] = catalogPriorityBase + index
	if levels := declaredReasoningLevels(model.ReasoningLevels); len(levels) > 0 {
		applyReasoningLevels(entry, levels, model.DefaultReasoningLevel)
	}
	if effort != "" {
		applyReasoningEffort(entry, effort)
	}
	if err := validateCatalogEntry(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// declaredReasoningLevels 把模型记录里声明的档位折算成 Codex 认识的规范档位：
// 丢掉未知值（拼错一个档位就会让 Codex 拒绝整份目录），并按官方从低到高排序，
// 与 cc-switch 的 codex_canonical_efforts 一致。
func declaredReasoningLevels(levels []string) []string {
	canonical := make([]string, 0, len(levels))
	for _, level := range reasoningEfforts {
		for _, declared := range levels {
			if strings.TrimSpace(declared) == level.effort {
				canonical = append(canonical, level.effort)
				break
			}
		}
	}
	return canonical
}

// applyReasoningLevels 用模型记录声明的档位替换模板档位，并选定默认档：
// 记录的默认档优先，其次保留模板默认档（仍在档位表内时），否则取最高档。
// 默认档永远不会指向被丢弃的未知档位。
func applyReasoningLevels(entry map[string]any, levels []string, declaredDefault string) {
	supported := make([]any, 0, len(levels))
	for _, level := range levels {
		description, _ := reasoningDescription(level)
		supported = append(supported, map[string]any{"effort": level, "description": description})
	}
	entry["supported_reasoning_levels"] = supported
	defaultLevel := ""
	switch declared := strings.TrimSpace(declaredDefault); {
	case declared != "" && containsEffort(levels, declared):
		defaultLevel = declared
	case containsEffort(levels, templateDefaultReasoningLevel(entry)):
		defaultLevel = templateDefaultReasoningLevel(entry)
	default:
		defaultLevel = levels[len(levels)-1]
	}
	entry["default_reasoning_level"] = defaultLevel
}

func containsEffort(levels []string, effort string) bool {
	for _, level := range levels {
		if level == effort {
			return true
		}
	}
	return false
}

func templateDefaultReasoningLevel(entry map[string]any) string {
	value, _ := entry["default_reasoning_level"].(string)
	return value
}

// applyReasoningEffort 保证 profile 里声明的 model_reasoning_effort 一定在目录的
// 支持档位内，否则 Codex 会认为该档位对当前模型不可用。
func applyReasoningEffort(entry map[string]any, effort string) {
	supported, ok := entry["supported_reasoning_levels"].([]any)
	if !ok {
		return
	}
	for _, item := range supported {
		if level, ok := item.(map[string]any); ok && level["effort"] == effort {
			entry["default_reasoning_level"] = effort
			return
		}
	}
	description, _ := reasoningDescription(effort)
	added := map[string]any{"effort": effort, "description": description}
	levels := make([]any, 0, len(supported)+1)
	inserted := false
	for _, item := range supported {
		level, _ := item.(map[string]any)
		current, _ := level["effort"].(string)
		if !inserted && effortOrder(current) > effortOrder(effort) {
			levels = append(levels, added)
			inserted = true
		}
		levels = append(levels, item)
	}
	if !inserted {
		levels = append(levels, added)
	}
	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = effort
}

func effortOrder(effort string) int {
	for index, level := range reasoningEfforts {
		if level.effort == effort {
			return index
		}
	}
	return len(reasoningEfforts)
}

// validateCatalogEntry 是渲染前自检：宁可报错，也不写出 Codex 会拒绝加载的目录。
func validateCatalogEntry(entry map[string]any) error {
	for _, field := range catalogRequiredFields {
		if _, ok := entry[field]; !ok {
			return fmt.Errorf("Codex 模型目录缺少必需字段 %s", field)
		}
	}
	if slug, _ := entry["slug"].(string); strings.TrimSpace(slug) == "" {
		return fmt.Errorf("Codex 模型目录缺少模型 ID")
	}
	if _, ok := entry["base_instructions"].(string); !ok {
		return fmt.Errorf("Codex 模型目录缺少 base_instructions")
	}
	levels, ok := entry["supported_reasoning_levels"].([]any)
	if !ok || len(levels) == 0 {
		return fmt.Errorf("Codex 模型目录缺少推理档位")
	}
	return nil
}
