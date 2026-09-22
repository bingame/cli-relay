package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/provider"
)

// catalogEntryShape 复刻 Codex 目录解析器最严格的一面：这些字段缺一个就会拒绝加载
// （0.155.1 实测 "missing field `slug`" / "missing field `base_instructions`"）。
type catalogEntryShape struct {
	Slug                    string           `json:"slug"`
	DisplayName             string           `json:"display_name"`
	BaseInstructions        string           `json:"base_instructions"`
	ShellType               string           `json:"shell_type"`
	Visibility              string           `json:"visibility"`
	SupportedInAPI          bool             `json:"supported_in_api"`
	SupportVerbosity        bool             `json:"support_verbosity"`
	Priority                int              `json:"priority"`
	ContextWindow           int64            `json:"context_window"`
	MaxContextWindow        int64            `json:"max_context_window"`
	SupportedReasoningLevel []map[string]any `json:"supported_reasoning_levels"`
	TruncationPolicy        map[string]any   `json:"truncation_policy"`
	ExperimentalTools       []any            `json:"experimental_supported_tools"`
}

func renderedCatalog(t *testing.T, p provider.Provider, models ...provider.Model) map[string]any {
	t.Helper()
	a := New()
	a.NativeHome = t.TempDir()
	artifact, err := a.Render(p, t.TempDir(), models...)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(artifact.Path), p.ID+".catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("目录不是合法 JSON: %v", err)
	}
	return catalog
}

func catalogEntries(t *testing.T, catalog map[string]any) []map[string]any {
	t.Helper()
	raw, ok := catalog["models"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatal("目录缺少 models 数组")
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatal("目录条目不是对象")
		}
		entries = append(entries, entry)
	}
	return entries
}

func decodeCatalogEntry(t *testing.T, entry map[string]any) catalogEntryShape {
	t.Helper()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var shape catalogEntryShape
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	return shape
}

func TestCatalogUsesNativeCodexShape(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	models := []provider.Model{
		{ProviderID: p.ID, ModelID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", ContextWindow: 262144, IsDefault: true, SortOrder: 0},
		{ProviderID: p.ID, ModelID: "deepseek-v4-pro", SortOrder: 1},
	}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	if len(entries) != 2 {
		t.Fatalf("条目数不符: %d", len(entries))
	}
	first := decodeCatalogEntry(t, entries[0])
	if first.Slug != "deepseek-v4-flash" || first.DisplayName != "DeepSeek V4 Flash" || !first.SupportedInAPI || first.SupportVerbosity || first.Visibility != "list" || first.ShellType != "shell_command" {
		t.Fatal("条目基础字段不符合 Codex 原生形态")
	}
	if first.ContextWindow != 262144 || first.MaxContextWindow != 262144 {
		t.Fatal("上下文窗口未按模型记录渲染")
	}
	if first.BaseInstructions == "" || len(first.SupportedReasoningLevel) == 0 || first.TruncationPolicy["mode"] != "bytes" || first.ExperimentalTools == nil {
		t.Fatal("条目缺少 Codex 加载所必需的字段")
	}
	second := decodeCatalogEntry(t, entries[1])
	if second.DisplayName != "deepseek-v4-pro" {
		t.Fatal("显示名未回落到模型 ID")
	}
	if first.Priority != catalogPriorityBase || second.Priority != catalogPriorityBase+1 {
		t.Fatal("优先级未按顺序生成")
	}
}

func TestCatalogOmitsFreeformToolDeclarations(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	models := []provider.Model{{ProviderID: p.ID, ModelID: "deepseek-v4-flash"}}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	for _, key := range []string{"apply_patch_tool_type", "web_search_tool_type", "tools", "model_messages"} {
		if _, ok := entries[0][key]; ok {
			t.Fatalf("第三方网关会拒绝 freeform 工具，不应声明 %s", key)
		}
	}
}

func TestCatalogAlwaysContainsProfileModel(t *testing.T) {
	p := sampleProvider()
	p.Model = "gpt-6-astra"
	models := []provider.Model{{ProviderID: p.ID, ModelID: "deepseek-v4-flash"}}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	if entries[0]["slug"] != "gpt-6-astra" {
		t.Fatal("profile 的 model 不在目录中")
	}
	if entries[0]["default_reasoning_level"] == nil {
		t.Fatal("条目缺少默认推理档位")
	}
}

// 只有默认模型、没有声明模型目录的渠道（cc-switch 里「模型映射」留空）必须交回
// Codex 自己发现模型列表：写了 model_catalog_json 就等于凭空造了一份映射。
func TestCatalogSkippedWithoutDeclaredModels(t *testing.T) {
	p := sampleProvider()
	p.Model = "gpt-6-astra"
	a := New()
	a.NativeHome = t.TempDir()
	artifact, err := a.Render(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := artifact.Profile["model_catalog_json"]; ok {
		t.Fatal("未声明模型目录时不应写 model_catalog_json")
	}
	if artifact.Profile["model"] != "gpt-6-astra" {
		t.Fatal("默认模型仍应写进 profile")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(artifact.Path), p.ID+".catalog.json")); !os.IsNotExist(err) {
		t.Fatal("未声明模型目录时不应生成目录文件")
	}
}

// 供应商从"声明过模型目录"变成"没声明"（或模型全部被清理）时，上一轮渲染留下的
// catalog 文件必须一并删掉，否则 rendered/ 里会留一份无人引用、内容误导的旧目录。
func TestCatalogRemovedWhenModelsNoLongerDeclared(t *testing.T) {
	p := sampleProvider()
	a := New()
	a.NativeHome = t.TempDir()
	root := t.TempDir()
	models := []provider.Model{{ProviderID: p.ID, ModelID: "declared-model"}}
	artifact, err := a.Render(p, root, models...)
	if err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(filepath.Dir(artifact.Path), p.ID+".catalog.json")
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatal("声明模型目录时应生成目录文件", err)
	}
	if _, err := a.Render(p, root); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{catalogPath, catalogPath + ".relay-cache.json"} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Fatalf("不再声明模型目录后应清理陈旧产物: %s", candidate)
		}
	}
}

func TestCatalogHonorsPerModelReasoningLevels(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	models := []provider.Model{
		{ProviderID: p.ID, ModelID: "m-declared", ReasoningLevels: []string{"bogus", "high", "low"}, DefaultReasoningLevel: "low"},
		{ProviderID: p.ID, ModelID: "m-default-dropped", ReasoningLevels: []string{"none", "xhigh"}, DefaultReasoningLevel: "medium"},
	}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	first := decodeCatalogEntry(t, entries[0])
	if !reflect.DeepEqual(effortsOf(first), []string{"low", "high"}) {
		t.Fatalf("声明档位未按官方顺序保留: %v", effortsOf(first))
	}
	if entries[0]["default_reasoning_level"] != "low" {
		t.Fatal("声明的默认档位未生效")
	}
	second := decodeCatalogEntry(t, entries[1])
	if !reflect.DeepEqual(effortsOf(second), []string{"none", "xhigh"}) {
		t.Fatalf("声明档位未替换模板档位: %v", effortsOf(second))
	}
	if entries[1]["default_reasoning_level"] != "xhigh" {
		t.Fatal("默认档不在声明表内时应取最高档")
	}
}

func effortsOf(entry catalogEntryShape) []string {
	efforts := make([]string, 0, len(entry.SupportedReasoningLevel))
	for _, level := range entry.SupportedReasoningLevel {
		efforts = append(efforts, level["effort"].(string))
	}
	return efforts
}

func TestCatalogContextWindowPrecedence(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	p.Extra = map[string]any{"codex_config": map[string]any{"model_context_window": 1000000}}
	models := []provider.Model{{ProviderID: p.ID, ModelID: "gpt-6-astra"}}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	if entries[0]["context_window"] != float64(1000000) {
		t.Fatal("未采用原生配置声明的上下文窗口")
	}
	p.Extra = nil
	entries = catalogEntries(t, renderedCatalog(t, p, models...))
	if entries[0]["context_window"] != float64(defaultCatalogContextWindow) {
		t.Fatal("缺少声明时未回落到默认窗口")
	}
}

func TestCatalogKeepsDeclaredReasoningEffort(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	p.Extra = map[string]any{"codex_config": map[string]any{"model_reasoning_effort": "medium"}}
	models := []provider.Model{{ProviderID: p.ID, ModelID: "gpt-6-astra"}}
	entry := catalogEntries(t, renderedCatalog(t, p, models...))[0]
	if entry["default_reasoning_level"] != "medium" {
		t.Fatal("默认推理档位未跟随原生配置")
	}
	var efforts []string
	for _, level := range entry["supported_reasoning_levels"].([]any) {
		efforts = append(efforts, level.(map[string]any)["effort"].(string))
	}
	if !reflect.DeepEqual(efforts, []string{"none", "medium", "high"}) {
		t.Fatalf("推理档位未按官方顺序补齐: %v", efforts)
	}
	if description := entry["supported_reasoning_levels"].([]any)[1].(map[string]any)["description"]; description == "" {
		t.Fatal("补齐的档位缺少描述")
	}
	p.Extra = map[string]any{"codex_config": map[string]any{"model_reasoning_effort": "bogus"}}
	entry = catalogEntries(t, renderedCatalog(t, p, models...))[0]
	if entry["default_reasoning_level"] != "high" {
		t.Fatal("未知档位不应写入目录")
	}
}

func TestCatalogEntriesAreIndependent(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	models := []provider.Model{
		{ProviderID: p.ID, ModelID: "model-a"},
		{ProviderID: p.ID, ModelID: "model-b"},
	}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	if entries[0]["slug"] == entries[1]["slug"] {
		t.Fatal("条目未独立生成")
	}
	entries[0]["supported_reasoning_levels"].([]any)[0].(map[string]any)["effort"] = "mutated"
	if entries[1]["supported_reasoning_levels"].([]any)[0].(map[string]any)["effort"] == "mutated" {
		t.Fatal("条目之间共享了嵌套结构")
	}
}

func TestCatalogDuplicateAndBlankModelsAreIgnored(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	models := []provider.Model{
		{ProviderID: p.ID, ModelID: "model-a"},
		{ProviderID: p.ID, ModelID: "model-a"},
		{ProviderID: p.ID, ModelID: "  "},
	}
	entries := catalogEntries(t, renderedCatalog(t, p, models...))
	if len(entries) != 1 || entries[0]["slug"] != "model-a" {
		t.Fatal("重复或空模型未被整理")
	}
}

// templateEntry 返回模板加渲染期注入的 priority，即 catalogEntry 产出的最小完整条目。
func templateEntry(t *testing.T) map[string]any {
	t.Helper()
	entry, err := parseTemplate()
	if err != nil {
		t.Fatal(err)
	}
	entry["priority"] = catalogPriorityBase
	return entry
}

func TestCatalogSkippedWhenNoUsableModel(t *testing.T) {
	p := sampleProvider()
	p.Model = ""
	a := New()
	a.NativeHome = t.TempDir()
	root := t.TempDir()
	models := []provider.Model{{ProviderID: p.ID, ModelID: "  "}}
	artifact, err := a.Render(p, root, models...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := artifact.Profile["model_catalog_json"]; ok {
		t.Fatal("没有可用模型时不应写 catalog")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(artifact.Path), p.ID+".catalog.json")); !os.IsNotExist(err) {
		t.Fatal("没有可用模型时不应生成目录文件")
	}
}

func TestValidateCatalogEntryRejectsIncompleteEntry(t *testing.T) {
	if err := validateCatalogEntry(templateEntry(t)); err != nil {
		t.Fatalf("完整条目被误拒绝: %v", err)
	}
	for _, testCase := range []struct {
		field string
		drop  string
	}{
		{field: "priority"},
		{field: "truncation_policy"},
		{field: "supported_reasoning_levels"},
		{field: "base_instructions"},
	} {
		entry := templateEntry(t)
		delete(entry, testCase.field)
		err := validateCatalogEntry(entry)
		if err == nil || !strings.Contains(err.Error(), testCase.field) {
			t.Fatalf("缺少 %s 时未拒绝渲染: %v", testCase.field, err)
		}
	}
	entry := templateEntry(t)
	entry["slug"] = "  "
	if err := validateCatalogEntry(entry); err == nil {
		t.Fatal("空模型 ID 时未拒绝渲染")
	}
	entry = templateEntry(t)
	entry["supported_reasoning_levels"] = []any{}
	if err := validateCatalogEntry(entry); err == nil {
		t.Fatal("空推理档位时未拒绝渲染")
	}
}
