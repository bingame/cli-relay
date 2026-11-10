package ccswitch

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestImportOnlyProviderData(t *testing.T) {
	for _, target := range []string{"claude", "codex", "gemini"} {
		t.Run(target, func(t *testing.T) {
			local := `C:\foreign-machine\local-only`
			settings := map[string]any{
				"api_key": "fake-provider-key", "base_url": "http://127.0.0.1:1234/v1", "model": "org/model",
				"projects":   map[string]any{local: map[string]any{"trust_level": "trusted"}},
				"hooks":      map[string]any{"command": local},
				"mcpServers": map[string]any{"server": map[string]any{"command": local, "token": "fake-mcp-token"}},
				"skills":     local, "enabledPlugins": map[string]any{local: true},
				"env": map[string]any{
					"ANTHROPIC_DEFAULT_HAIKU_MODEL": "org/small",
					"API_TIMEOUT_MS":                "60000", "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "8192",
					"PATH": local, "CLAUDE_CONFIG_DIR": local, "MCP_TOKEN": "fake-mcp-token",
					"ANTHROPIC_UNKNOWN_PATH": local,
					"HEADER_VAR":             "fake-provider-header",
				},
				"config": `model_provider = "custom"
model = "org/model"
model_reasoning_effort = "high"
model_catalog_json = 'C:\foreign-machine\catalog.json'
notify = ['C:\foreign-machine\notify.exe']
[projects.'C:\foreign-machine\local-only']
trust_level = "trusted"
[mcp_servers.local]
command = "/home/foreign/local-only/server"
[skills]
config = [{path = "/home/foreign/local-only/skill"}]
[model_providers.custom]
base_url = "http://127.0.0.1:1234/v1"
wire_api = "responses"
env_http_headers = {Authorization = "HEADER_VAR"}
auth = {command = "/home/foreign/local-only/auth"}
[model_providers.unused]
http_headers = {Authorization = "fake-unused-header"}
`,
			}
			raw, _ := json.Marshal(settings)
			row := validRow()
			row["app_type"] = target
			row["settings_config"] = string(raw)
			row["meta"] = `{"path":"C:\\foreign-machine\\meta"}`
			entry, err := parseEntry(row)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(entry)
			for _, forbidden := range []string{"foreign-machine", "local-only", "fake-mcp-token", "fake-unused-header", "source_settings", "source_meta", "ANTHROPIC_UNKNOWN_PATH"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("不应导入 %s", forbidden)
				}
			}
			if entry.Provider.BaseURL != "http://127.0.0.1:1234/v1" || entry.Provider.Model != "org/model" || entry.Secrets["api_key"] != "fake-provider-key" {
				t.Fatal("供应商地址、模型或凭据丢失")
			}
			if target == "claude" {
				env := entry.Provider.Extra["claude_settings"].(map[string]any)["env"].(map[string]any)
				if env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "org/small" || env["API_TIMEOUT_MS"] != "60000" || env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "8192" {
					t.Fatal("模型映射或请求参数丢失")
				}
			}
			if target == "codex" && entry.Secrets["env:HEADER_VAR"] != "fake-provider-header" {
				t.Fatal("供应商 header 凭据丢失")
			}
			if !reflect.DeepEqual(entry.Provider.Extra, providerOnly(entry.Provider).Extra) {
				t.Fatal("新导入与存量清理白名单不一致")
			}
		})
	}
}
