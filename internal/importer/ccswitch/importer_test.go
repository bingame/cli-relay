package ccswitch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
	if claude.Provider.ID != "claude-attach" || claude.Provider.DisplayName != "Claude '演示'; ATTACH" || !claude.Current || !claude.Provider.Supports("claude-code") {
		t.Fatal("Claude column or target mapping failed")
	}
	if claude.Provider.BaseURL != "https://claude.example.test" || claude.Provider.Model != "claude-example" {
		t.Fatal("Claude model mapping failed")
	}
	if claude.Secrets["api_key"] != "fake-claude-key" || claude.Secrets["env:OTHER_SECRET"] != "fake-extra-secret" || claude.Secrets["source_meta"] == "" {
		t.Fatal("Claude secrets were not preserved")
	}
	settings := claude.Provider.Extra["claude_settings"].(map[string]any)
	if settings["env"].(map[string]any)["SAFE_FLAG"] != "yes" || settings["permissions"] == nil {
		t.Fatal("nonsecret settings were lost")
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
	row["settings_config"] = `{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-known-value","OTHER_SECRET":"true"},"description":"uses fake-known-value","safe":"true","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo fake-known-value"},{"type":"command","command":"echo harmless"}]}]},"fake-known-value":"secret field name"}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(entry.Provider)
	warnings, _ := json.Marshal(entry.Warnings)
	if strings.Contains(string(encoded), "fake-known-value") || strings.Contains(string(warnings), "fake-known-value") || len(entry.Warnings) != 3 {
		t.Fatal("known credential survived public configuration filtering")
	}
	settings := entry.Provider.Extra["claude_settings"].(map[string]any)
	if settings["safe"] != "true" || !strings.Contains(string(encoded), "echo harmless") {
		t.Fatal("short noncredential environment values caused unrelated data loss")
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
		for _, target := range entry.Provider.Targets {
			targets[target]++
		}
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

func validRow() map[string]any {
	return map[string]any{"id": "test", "app_type": "claude", "name": "测试", "settings_config": "{}", "meta": "{}", "is_current": int64(0)}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
