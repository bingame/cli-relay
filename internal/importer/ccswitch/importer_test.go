package ccswitch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/provider"
)

func TestParseRealisticDump(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "ccswitch", "sample.sql")
	// 对外路径禁止父级跳转；测试先解析为绝对路径。
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 3 || len(result.Columns) != 7 {
		t.Fatalf("unexpected counts: providers=%d, columns=%d", len(result.Providers), len(result.Columns))
	}
	content, _ := os.ReadFile(path)
	hash := sha256.Sum256(content)
	if result.FileHash != hex.EncodeToString(hash[:]) {
		t.Fatal("file hash does not match original bytes")
	}
	claude := result.Providers[0]
	if claude.Provider.ID != "claude-attach" || claude.Provider.DisplayName != "Claude '演示'; ATTACH" || !claude.Current || !claude.Provider.Supports("claude") {
		t.Fatal("Claude column or target mapping failed")
	}
	if claude.Provider.BaseURL != "https://claude.example.test" || claude.Provider.Model != "claude-example" {
		t.Fatal("Claude model mapping failed")
	}
	if claude.Secrets["api_key"] != "fake-claude-key" || claude.Secrets["env:OTHER_SECRET"] != "fake-extra-secret" || claude.Secrets["source_meta"] == "" {
		t.Fatal("Claude secrets were not preserved")
	}
	settings := claude.Provider.Extra["claude_settings"].(map[string]any)
	if settings["env"].(map[string]any)["SAFE_FLAG"] != "yes" {
		t.Fatal("nonsecret settings were lost")
	}
	if settings["permissions"] != nil || settings["hooks"] != nil {
		t.Fatal("spec §6 白名单：permissions/hooks 等运行环境配置不应入库")
	}
	codex := result.Providers[1]
	if codex.Provider.ID != "codex" || codex.Provider.BaseURL != "https://codex.example.test/v1" || codex.Provider.Model != "gpt-example" || !codex.Provider.Supports("codex") || codex.Current {
		t.Fatal("Codex selected provider mapping failed")
	}
	if codex.Secrets["api_key"] != "fake-codex-key" || codex.Secrets["env:MY_API_KEY"] != "fake-env-key" {
		t.Fatal("Codex secrets mapping failed")
	}
	config := codex.Provider.Extra["codex_config"].(map[string]any)
	nativeProvider := config["model_providers"].(map[string]any)["custom"].(map[string]any)
	if nativeProvider["env_http_headers"] == nil || nativeProvider["http_headers"] != nil || nativeProvider["experimental_bearer_token"] != nil {
		t.Fatal("Codex headers were not separated from credential references")
	}
	other := result.Providers[2]
	if !other.Provider.Supports("gemini") || !provider.ValidID(other.Provider.ID) || other.OriginalID != "../无效 ID" || other.Secrets["source_settings"] == "" {
		t.Fatal("unsupported target or original ID was lost")
	}
	for _, entry := range result.Providers {
		encoded, err := json.Marshal(entry.Provider)
		if err != nil || strings.Contains(string(encoded), "fake-") {
			t.Fatal("provider metadata leaked fixture credentials")
		}
	}
	var snapshot map[string]rawTable
	if err := json.Unmarshal(result.Snapshot, &snapshot); err != nil || len(snapshot["mcp_servers"].Rows) != 1 || len(snapshot["prompts"].Rows) != 1 {
		t.Fatal("raw table snapshot missing")
	}
	if !strings.Contains(string(result.Snapshot), "fake-mcp-secret") || snapshot["prompts"].Rows[0]["content"] != "多行\n提示正文" {
		t.Fatal("raw snapshot must preserve original data for encrypted persistence")
	}
}

func TestParseRejectsUnsafeSQLWithoutFilesystemEffects(t *testing.T) {
	for _, attack := range []string{
		`ATTACH DATABASE 'attack.db' AS escaped;`,
		`/* comment */ aTtAcH /* comment */ 'attack.db' AS escaped;`,
		`DETACH DATABASE main;`,
		`VACUUM INTO 'attack.db';`,
		`VACUUM;`,
		`PRAGMA temp_store_directory='attack';`,
		`PRAGMA "writable_schema"=ON;`,
		`PRAGMA main.user_version=1;`,
		`SELECT load_extension('attack');`,
		`INSERT INTO providers SELECT "load_extension"('attack');`,
		"INSERT INTO providers SELECT `load_extension`('attack');",
		`INSERT INTO providers SELECT [load_extension]('attack');`,
		`INSERT INTO providers SELECT 'load_extension'('attack');`,
		`INSERT INTO providers SELECT writefile('attack.db','fake-secret');`,
		`CREATE VIRTUAL TABLE leak USING csv(filename='attack.db');`,
		`CREATE TRIGGER leak AFTER INSERT ON providers BEGIN SELECT writefile('attack.db', 'fake-secret'); END;`,
		`CREATE TEMP TRIGGER leak AFTER INSERT ON providers BEGIN DELETE FROM providers; END;`,
		`CREATE TABLE fake AS SELECT readfile('attack.db');`,
	} {
		t.Run(attack, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "import.sql")
			writeFile(t, path, exportHeader+"\n"+attack)
			_, err := Parse(context.Background(), path)
			if err == nil {
				t.Fatal("unsafe SQL accepted")
			}
			if strings.Contains(err.Error(), "fake-secret") || strings.Contains(err.Error(), "attack.db") {
				t.Fatal("error includes source SQL content")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("unsafe SQL created a file")
			}
		})
	}
}

func TestParseRejectsDamagedAndIncompleteDumps(t *testing.T) {
	for name, script := range map[string]string{
		"wrong-header":           "-- CC_SWITCH_SQL_EXPORT_HEADER\n" + minimalSQL,
		"header-prefix":          exportHeader + "-not-an-export\n" + minimalSQL,
		"incomplete-transaction": exportHeader + "\nBEGIN;" + minimalSQL,
		"incomplete-savepoint":   exportHeader + "\nSAVEPOINT unfinished;" + minimalSQL,
		"incomplete-string":      exportHeader + "\n" + minimalSQL + "INSERT INTO providers VALUES ('fake-sensitive",
		"incomplete-comment":     exportHeader + "\n" + minimalSQL + "/* unfinished",
		"invalid-sql":            exportHeader + "\n" + minimalSQL + "INSERT INTO providers VALUES ('fake-sensitive');",
		"missing-table":          exportHeader + "\nCREATE TABLE other (id TEXT);",
		"view-instead-of-table":  exportHeader + "\nCREATE VIEW providers AS SELECT 1;",
		"invalid-utf8":           exportHeader + "\n" + minimalSQL + "--" + string([]byte{0xff}),
		"nul":                    exportHeader + "\n" + minimalSQL + "\x00",
		"nul-in-comment":         exportHeader + "\n" + minimalSQL + "-- comment\x00",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "import.sql")
			writeFile(t, path, script)
			_, err := Parse(context.Background(), path)
			if err == nil {
				t.Fatal("damaged SQL accepted")
			}
			if strings.Contains(err.Error(), "fake-sensitive") {
				t.Fatal("error leaked SQL literal")
			}
		})
	}
}

func TestMissingColumnReportsActualNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import.sql")
	writeFile(t, path, exportHeader+"\nPRAGMA user_version=77; CREATE TABLE providers (id TEXT, future_column TEXT);")
	_, err := Parse(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "future_column") || !strings.Contains(err.Error(), "所属应用类型") || !strings.Contains(err.Error(), "user_version=77") {
		t.Fatal("missing-column diagnostic does not report schema")
	}
}

func TestParseBuildsProviderQueryFromPragmaSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import.sql")
	writeFile(t, path, exportHeader+`
PRAGMA user_version=23;
CREATE TABLE providers (
    future_column TEXT,
    is_current INTEGER NOT NULL DEFAULT 0,
    settings_config TEXT NOT NULL,
    name TEXT NOT NULL,
    app_type TEXT NOT NULL,
    id TEXT NOT NULL
);
INSERT INTO providers (id, app_type, name, settings_config, is_current, future_column)
VALUES ('source-id', 'claude', '现场 Schema', '{"env":{"ANTHROPIC_MODEL":"claude-schema"}}', 1, 'ignored');
`)
	result, err := Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 1 {
		t.Fatalf("expected one provider, got %d", len(result.Providers))
	}
	entry := result.Providers[0]
	if entry.OriginalID != "source-id" || entry.Provider.DisplayName != "现场 Schema" || entry.Provider.Model != "claude-schema" || !entry.Current {
		t.Fatal("provider semantic mapping did not follow the probed schema")
	}
	wantColumns := []string{"future_column", "is_current", "settings_config", "name", "app_type", "id"}
	if strings.Join(result.Columns, ",") != strings.Join(wantColumns, ",") {
		t.Fatalf("schema column order was not preserved: %v", result.Columns)
	}
	if _, exists := entry.Secrets["source_meta"]; exists {
		t.Fatal("optional metadata was fabricated when the probed schema did not contain it")
	}
}

func TestExplicitSelectQueryNeverUsesWildcardOrUnquotedIdentifiers(t *testing.T) {
	query, err := explicitSelectQuery("providers", []string{"id", `future"column`})
	if err != nil {
		t.Fatal(err)
	}
	if query != `SELECT "id", "future""column" FROM "providers"` || strings.Contains(query, "*") {
		t.Fatalf("unexpected explicit-column query: %s", query)
	}
	if _, err := explicitSelectQuery("providers", nil); err == nil {
		t.Fatal("empty explicit-column query was accepted")
	}
}

func TestParseEmptyDumpWithBOMAndNoOptionalTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import.sql")
	writeFile(t, path, "\ufeff"+exportHeader+"\r\n"+minimalSQL)
	result, err := Parse(context.Background(), path)
	if err != nil || len(result.Providers) != 0 || result.Snapshot == nil {
		t.Fatal("valid empty export failed")
	}
}

func TestParseOfficialAuthAndBearerFallback(t *testing.T) {
	for _, settings := range []string{
		`{"auth":{},"config":"model = \"official\""}`,
		`{"config":"model_provider=\"p\"\n[model_providers.p]\nexperimental_bearer_token=\"fake-bearer\""}`,
	} {
		row := validRow()
		row["app_type"] = "codex"
		row["settings_config"] = settings
		entry, err := parseEntry(row)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(settings, "fake-bearer") && entry.Secrets["api_key"] != "fake-bearer" {
			t.Fatal("bearer credential fallback missing")
		}
		encoded, _ := json.Marshal(entry.Provider)
		if strings.Contains(string(encoded), "fake-bearer") {
			t.Fatal("bearer credential leaked")
		}
	}
}

func TestClaudePreservesNativeAuthMode(t *testing.T) {
	for _, test := range []struct {
		settings string
		envName  string
		key      string
	}{
		{`{"env":{"ANTHROPIC_API_KEY":"fake-api-key-only"}}`, "ANTHROPIC_API_KEY", "fake-api-key-only"},
		{`{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-auth-token-only"}}`, "ANTHROPIC_AUTH_TOKEN", "fake-auth-token-only"},
		{`{"env":{"ANTHROPIC_API_KEY":"fake-api-key-secondary","ANTHROPIC_AUTH_TOKEN":"fake-auth-token-primary"}}`, "ANTHROPIC_AUTH_TOKEN", "fake-auth-token-primary"},
	} {
		row := validRow()
		row["settings_config"] = test.settings
		entry, err := parseEntry(row)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Secrets["api_key_env"] != test.envName || entry.Secrets["api_key"] != test.key {
			t.Fatal("Claude native authentication mode was not preserved")
		}
		if entry.Secrets["env:ANTHROPIC_API_KEY"] != "" || entry.Secrets["env:ANTHROPIC_AUTH_TOKEN"] != "" {
			t.Fatal("Claude primary authentication was duplicated")
		}
	}
}

func TestCodexEnvironmentKeyAndHTTPHeaders(t *testing.T) {
	row := validRow()
	row["app_type"] = "codex"
	row["settings_config"] = `{"config":"model_provider=\"p\"\n[model_providers.p]\nenv_key=\"CUSTOM_GATEWAY\"\nhttp_headers={ X-Other=\"fake-inline-header\" }\nenv_http_headers={ Authorization=\"AUTH_HEADER\" }", "env":{"CUSTOM_GATEWAY":"fake-custom-main","AUTH_HEADER":"fake-reference-value"}}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Secrets["api_key"] != "fake-custom-main" || entry.Secrets["env:AUTH_HEADER"] != "fake-reference-value" {
		t.Fatal("explicit credential environment references were not extracted")
	}
	config := entry.Provider.Extra["codex_config"].(map[string]any)
	native := config["model_providers"].(map[string]any)["p"].(map[string]any)
	refs := native["env_http_headers"].(map[string]any)
	if refs["Authorization"] != "AUTH_HEADER" || native["http_headers"] != nil {
		t.Fatal("header references were not preserved")
	}
	variable, ok := refs["X-Other"].(string)
	if !ok || entry.Secrets["env:"+variable] != "fake-inline-header" {
		t.Fatal("inline header was not converted to an environment reference")
	}
	encoded, _ := json.Marshal(entry.Provider)
	if strings.Contains(string(encoded), "fake-") {
		t.Fatal("Codex header credential leaked")
	}
}

func TestSanitizeRetainsNumericTokenBudgetsAndExtractsCustomHeaders(t *testing.T) {
	secrets := map[string]string{}
	clean := sanitize(map[string]any{
		"model_auto_compact_token_limit": int64(100000),
		"max_output_tokens":              float64(1024),
		"env":                            map[string]any{"ANTHROPIC_CUSTOM_HEADERS": "Authorization: fake-custom-header"},
	}, "", secrets).(map[string]any)
	if clean["model_auto_compact_token_limit"] != int64(100000) || clean["max_output_tokens"] != float64(1024) {
		t.Fatal("numeric token budgets must not be mistaken for credentials")
	}
	if secrets["env:ANTHROPIC_CUSTOM_HEADERS"] != "Authorization: fake-custom-header" || len(clean["env"].(map[string]any)) != 0 {
		t.Fatal("custom HTTP header literal was not isolated")
	}
}

func TestKnownCredentialsCannotBeCopiedToPublicConfiguration(t *testing.T) {
	row := validRow()
	row["settings_config"] = `{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-known-value","OTHER_SECRET":"true","SAFE_FLAG":"yes"},"description":"uses fake-known-value","safe":"true","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo fake-known-value"},{"type":"command","command":"echo harmless"}]}]},"fake-known-value":"secret field name"}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(entry.Provider)
	warnings, _ := json.Marshal(entry.Warnings)
	if strings.Contains(string(encoded), "fake-known-value") || strings.Contains(string(warnings), "fake-known-value") || len(entry.Warnings) != 0 {
		t.Fatal("known credential survived public configuration filtering")
	}
	settings := entry.Provider.Extra["claude_settings"].(map[string]any)
	if settings["env"].(map[string]any)["SAFE_FLAG"] != "yes" {
		t.Fatal("白名单应保留 env 中的非凭据语义字段")
	}
	if settings["safe"] != nil || settings["hooks"] != nil || settings["description"] != nil || strings.Contains(string(encoded), "echo harmless") {
		t.Fatal("spec §6 白名单：hooks 等运行环境配置应整体剔除，含无害内容")
	}
	if !strings.Contains(entry.Secrets["source_settings"], "echo fake-known-value") {
		t.Fatal("encrypted source backup lost original hook")
	}
	for _, field := range []string{"id", "name", "app_type"} {
		bad := validRow()
		bad["settings_config"] = `{"api_key":"xyz","env":{"ANTHROPIC_AUTH_TOKEN":"xyz"}}`
		bad[field] = "prefix-xyz-suffix"
		if _, err := parseEntry(bad); err == nil || strings.Contains(err.Error(), "xyz") {
			t.Fatal("public field containing a short primary credential was accepted or leaked")
		}
	}
}

func TestCredentialCopyInsideWhitelistedFieldIsStripped(t *testing.T) {
	row := validRow()
	row["settings_config"] = `{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-known-value","SAFE_NOTE":"prefix fake-known-value suffix","SAFE_FLAG":"yes"}}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	env := entry.Provider.Extra["claude_settings"].(map[string]any)["env"].(map[string]any)
	if env["SAFE_FLAG"] != "yes" {
		t.Fatal("无关字段被误删")
	}
	if env["SAFE_NOTE"] != nil || len(entry.Warnings) != 1 {
		t.Fatal("白名单字段内的凭据副本未被剥离")
	}
	encoded, _ := json.Marshal(entry.Provider)
	if strings.Contains(string(encoded), "fake-known-value") {
		t.Fatal("凭据副本进入公开元数据")
	}
}

func TestCodexConfigWhitelistDropsExecutionEnvironment(t *testing.T) {
	row := validRow()
	row["app_type"] = "codex"
	row["settings_config"] = `{"config":"model_provider=\"p\"\nmodel=\"gpt-example\"\nsandbox_mode=\"workspace-write\"\napproval_policy=\"never\"\nshell_environment_policy={ inherit = \"all\" }\nmodel_reasoning_effort=\"high\"\n[model_providers.p]\nname=\"p\"\nbase_url=\"https://p.example.test/v1\"\nwire_api=\"responses\"\nrequest_max_retries=3\nexperimental_key=\"x\""}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	config := entry.Provider.Extra["codex_config"].(map[string]any)
	if config["model_provider"] != "p" || config["model"] != "gpt-example" || config["model_reasoning_effort"] != "high" {
		t.Fatal("白名单应保留模型与供应商选择字段")
	}
	for _, key := range []string{"sandbox_mode", "approval_policy", "shell_environment_policy"} {
		if config[key] != nil {
			t.Fatalf("spec §6 白名单：%s 不应入库", key)
		}
	}
	def := config["model_providers"].(map[string]any)["p"].(map[string]any)
	if def["base_url"] != "https://p.example.test/v1" || def["request_max_retries"] != int64(3) {
		t.Fatal("白名单应保留供应商连接字段")
	}
	if def["experimental_key"] != nil {
		t.Fatal("供应商定义内的非白名单字段不应入库")
	}
}

func TestEntryRejectsMalformedConfigurationsWithoutLeaks(t *testing.T) {
	for name, fieldValue := range map[string]map[string]any{
		"json":      {"settings_config": "{fake-sensitive"},
		"null":      {"settings_config": "null"},
		"toml":      {"app_type": "codex", "settings_config": `{"config":"fake-sensitive=["}`},
		"toml-type": {"app_type": "codex", "settings_config": `{"config":123}`},
		"current":   {"is_current": int64(8)},
		"type":      {"app_type": "../bad"},
		"url":       {"settings_config": `{"env":{"ANTHROPIC_BASE_URL":"https://fake-sensitive@example.test"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			row := validRow()
			for key, value := range fieldValue {
				row[key] = value
			}
			_, err := parseEntry(row)
			if err == nil || strings.Contains(err.Error(), "fake-sensitive") {
				t.Fatal("configuration validation failed or leaked source")
			}
		})
	}
}

func TestParseCancellationAndSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import.sql")
	writeFile(t, path, exportHeader+"\n"+minimalSQL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, path); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxDumpBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := Parse(context.Background(), path); err == nil {
		t.Fatal("oversize dump accepted")
	}
}

func TestParseRealDumpStatistics(t *testing.T) {
	path := os.Getenv("RELAY_TEST_CCSWITCH_DUMP")
	if path == "" {
		t.Skip("显式指定真实 dump 路径后才运行；只输出计数")
	}
	result, err := Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]int{}
	for _, entry := range result.Providers {
		targets[entry.Provider.Target]++
		encoded, err := json.Marshal(entry.Provider)
		if err != nil {
			t.Fatal("供应商元数据编码失败")
		}
		for key, secret := range entry.Secrets {
			if key == "source_settings" || key == "source_meta" || key == "api_key_env" || len(secret) < 8 {
				continue
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatal("检测到凭据进入未加密元数据")
			}
		}
	}
	t.Logf("供应商总数：%d；目标分布：%v；列数：%d", len(result.Providers), targets, len(result.Columns))
}

const minimalSQL = `CREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, meta TEXT, is_current INTEGER);`

func TestCodexModelCatalogImport(t *testing.T) {
	row := validRow()
	row["app_type"] = "codex"
	row["settings_config"] = `{"auth":{"OPENAI_API_KEY":"fake-codex-key"},` +
		`"config":"model_provider = \"custom\"\nmodel = \"gpt-6-astra\"\n[model_providers.custom]\nbase_url = \"https://gateway.example.test/v1\"\nwire_api = \"responses\"",` +
		`"modelCatalog":{"models":[` +
		`{"model":"deepseek-flash-latest","displayName":"DeepSeek Flash","contextWindow":256000,"reasoningLevels":["high","low","high"],"defaultReasoningLevel":"low"},` +
		`{"model":" glm-flash-latest "},` +
		`{"model":"deepseek-flash-latest"},` +
		`{"model":""}]}}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Provider.Model != "gpt-6-astra" {
		t.Fatal("默认模型未从原生配置读取")
	}
	if len(entry.Models) != 2 {
		t.Fatalf("模型映射未按 cc-switch 语义整理（空模型名与重复行应跳过）: %+v", entry.Models)
	}
	first := entry.Models[0]
	if first.ModelID != "deepseek-flash-latest" || first.DisplayName != "DeepSeek Flash" || first.ContextWindow != 256000 || first.SortOrder != 0 {
		t.Fatalf("模型映射可编辑字段未导入: %+v", first)
	}
	if !reflect.DeepEqual(first.ReasoningLevels, []string{"high", "low"}) || first.DefaultReasoningLevel != "low" {
		t.Fatalf("思考档位未导入: %+v", first)
	}
	if first.IsDefault {
		t.Fatal("默认模型不在映射中时不应把映射行当作默认")
	}
	second := entry.Models[1]
	if second.ModelID != "glm-flash-latest" || second.DisplayName != "" || second.ContextWindow != 0 || second.SortOrder != 1 || len(second.ReasoningLevels) != 0 {
		t.Fatalf("未声明字段不应编造: %+v", second)
	}
	// modelCatalog 是公开元数据；凭据不得混进来。
	if encoded, _ := json.Marshal(entry.Provider); strings.Contains(string(encoded), "fake-codex-key") {
		t.Fatal("模型目录泄漏了凭据")
	}
}

// 回归：cc-switch 里「模型映射」留空的渠道在 Relay 侧不能凭空得到一份映射。
// 旧实现用 provider.Model 伪造一条 provider_models，导致 Codex 的 /model 菜单被锁成单个模型。
func TestImportDoesNotInventModelCatalog(t *testing.T) {
	codexRow := validRow()
	codexRow["app_type"] = "codex"
	codexRow["settings_config"] = `{"config":"model_provider=\"custom\"\nmodel = \"gpt-6-astra\"\n[model_providers.custom]\nbase_url=\"https://gateway.example.test/v1\"\nwire_api=\"responses\""}`
	entry, err := parseEntry(codexRow)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Provider.Model != "gpt-6-astra" || len(entry.Models) != 0 {
		t.Fatalf("未声明模型映射的 Codex 渠道不得生成模型目录: %+v", entry.Models)
	}
	// 空 modelCatalog 与键缺失同样是"没有声明"。
	codexRow["settings_config"] = `{"config":"model = \"gpt-6-astra\"","modelCatalog":{"models":[]}}`
	entry, err = parseEntry(codexRow)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Models) != 0 {
		t.Fatalf("空模型映射不得生成模型目录: %+v", entry.Models)
	}
	claudeRow := validRow()
	claudeRow["settings_config"] = `{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-claude-key","ANTHROPIC_MODEL":"claude-opus-5"}}`
	entry, err = parseEntry(claudeRow)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Provider.Model != "claude-opus-5" || len(entry.Models) != 0 {
		t.Fatalf("Claude Code 的模型映射走 env，不应另造模型目录: %+v", entry.Models)
	}
}

func validRow() map[string]any {
	return map[string]any{"id": "test", "app_type": "claude", "name": "测试", "settings_config": "{}", "meta": "{}", "is_current": int64(0)}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
