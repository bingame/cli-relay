package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/provider"
	"github.com/bingame/cli-relay/internal/secrets"
)

func TestImportCleansHistoricalLocalConfiguration(t *testing.T) {
	for _, mode := range []string{"overwrite", "skip", "empty"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, native := t.TempDir(), t.TempDir()
			passphrase := "fake-import-cleanup-passphrase"
			t.Setenv("RELAY_PASSPHRASE", passphrase)
			t.Setenv("CODEX_HOME", native)
			t.Setenv("CLAUDE_CONFIG_DIR", native)
			nativePath := filepath.Join(native, "config.toml")
			nativeData := []byte("[projects.'/native/project']\ntrust_level='trusted'\n")
			if err := os.WriteFile(nativePath, nativeData, 0600); err != nil {
				t.Fatal(err)
			}
			vault, err := secrets.Open(root, secrets.Options{Passphrase: passphrase})
			if err != nil {
				t.Fatal(err)
			}
			openStore := func() *provider.Store {
				t.Helper()
				store, err := provider.Open(root, vault)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				return store
			}
			legacyExtra := map[string]any{
				"codex_config": map[string]any{
					"model_provider": "custom", "model_reasoning_effort": "high",
					"projects":           map[string]any{"C:/old-machine/project": map[string]any{"trust_level": "trusted"}},
					"mcp_servers":        map[string]any{"server": map[string]any{"command": "/old-machine/server"}},
					"skills":             map[string]any{"path": "/old-machine/skill"},
					"model_catalog_json": "/old-machine/catalog.json",
					"model_providers": map[string]any{"custom": map[string]any{
						"wire_api": "responses", "env_http_headers": map[string]any{"X-Gateway": "GATEWAY_HEADER"},
						"auth": map[string]any{"command": "/old-machine/auth"},
					}},
				},
				"local_path": "/old-machine/extra",
			}
			legacy := provider.Provider{ID: "legacy", DisplayName: "Legacy", Target: "codex", Source: "cc-switch-import",
				BaseURL: "https://old.example.test/v1", Model: "org/old-model", Extra: legacyExtra}
			stale := provider.Provider{ID: "stale", DisplayName: "Stale", Target: "claude", Source: "cc-switch-import", Status: "disabled",
				Model: "old-claude", Extra: map[string]any{"claude_settings": map[string]any{
					"hooks": map[string]any{"command": "/old-machine/hook"},
					"env":   map[string]any{"PATH": "/old-machine/bin", "CLAUDE_CONFIG_DIR": "/old-machine/config", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "org/small"},
				}}}
			unknown := provider.Provider{ID: "unknown", DisplayName: "Unknown", Target: "gemini", Source: "cc-switch-import",
				Extra: map[string]any{"skills": "/old-machine/skills"}}
			manual := legacy
			manual.ID, manual.DisplayName, manual.Source = "manual", "Manual", "manual"
			models := []provider.Model{{ProviderID: legacy.ID, ModelID: "org/old-model", DisplayName: "旧模型", IsDefault: true}}
			legacySecrets := map[string]string{"api_key": "fake-old-key", "env:GATEWAY_HEADER": "fake-header",
				"source_settings": `{"projects":"/old-machine/project"}`, "source_meta": "/old-machine/meta",
				"env:MCP_TOKEN": "fake-local-token", "field:hooks.command": "/old-machine/hook"}
			store := openStore()
			err = store.Import(ctx, []provider.Entry{
				{Provider: legacy, Secrets: legacySecrets, Models: models},
				{Provider: stale, Secrets: map[string]string{"api_key": "fake-claude-key", "api_key_env": "ANTHROPIC_API_KEY", "source_settings": "/old-machine/settings"}},
				{Provider: unknown, Secrets: map[string]string{"api_key": "fake-unknown-key", "source_settings": "/old-machine/settings"}},
				{Provider: manual, Secrets: legacySecrets},
			}, "old-import-hash", []byte(`{"mcp_servers":"/old-machine/server"}`))
			if err != nil {
				t.Fatal(err)
			}
			originalLegacy, _ := store.Get(ctx, "codex", "legacy")
			originalManual, _ := store.Get(ctx, "codex", "manual")
			store.Close()
			renderPath := filepath.Join(root, "rendered", "claude", "stale.json")
			if err = os.MkdirAll(filepath.Dir(renderPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(renderPath, []byte(`{"env":{"PATH":"/old-machine/bin"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			dump := "-- CC Switch SQLite 导出\nCREATE TABLE providers(id TEXT, app_type TEXT, name TEXT, settings_config TEXT, is_current INTEGER);\n"
			if mode != "empty" {
				dump += `INSERT INTO providers VALUES('upstream','codex','Legacy','{"auth":{"OPENAI_API_KEY":"fake-new-key"},"config":"model_provider=\"custom\"\nmodel=\"org/new-model\"\n[model_providers.custom]\nbase_url=\"https://new.example.test/v1\""}',0);`
			}
			input := filepath.Join(t.TempDir(), "dump.sql")
			if err = os.WriteFile(input, []byte(dump), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"provider", "import", "--from", "cc-switch", input}
			if mode == "skip" {
				args = append(args, "--on-conflict", "skip")
			}
			databasePath := filepath.Join(root, "providers.db")
			before, _ := os.ReadFile(databasePath)
			// dry-run 必须在不解锁凭据库的情况下展示完整清理计划。
			t.Setenv("RELAY_PASSPHRASE", "deliberately-wrong-passphrase")
			out, err := command(t, root, "", append(append([]string{}, args...), "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Cleaned   []string `json:"cleaned_providers"`
				Snapshots int      `json:"discarded_snapshots"`
			}
			if err = json.Unmarshal([]byte(out), &report); err != nil || !reflect.DeepEqual(report.Cleaned, []string{"claude/stale", "codex/legacy", "gemini/unknown"}) || report.Snapshots != 1 {
				t.Fatal("dry-run 清理计划不正确", out, err)
			}
			after, _ := os.ReadFile(databasePath)
			render, _ := os.ReadFile(renderPath)
			if !bytes.Equal(before, after) || !strings.Contains(string(render), "/old-machine/bin") {
				t.Fatal("dry-run 修改了存储或产物")
			}
			t.Setenv("RELAY_PASSPHRASE", passphrase)
			if _, err = command(t, root, "", args...); err != nil {
				t.Fatal(err)
			}
			store = openStore()
			items, err := store.List(ctx, "")
			if err != nil || len(items) != 4 {
				t.Fatal("清理不应删除供应商", err)
			}
			for _, p := range items {
				gotSecrets, err := store.Secrets(ctx, p.Target, p.ID)
				if err != nil {
					t.Fatal(err)
				}
				if p.Source == "manual" {
					if !reflect.DeepEqual(p, originalManual) || !reflect.DeepEqual(gotSecrets, legacySecrets) {
						t.Fatal("清理修改了手工配置")
					}
					continue
				}
				data, _ := json.Marshal(p)
				if strings.Contains(string(data), "old-machine") || gotSecrets["source_settings"] != "" || gotSecrets["source_meta"] != "" || gotSecrets["env:MCP_TOKEN"] != "" || gotSecrets["field:hooks.command"] != "" {
					t.Fatal("历史非供应商数据未清理")
				}
				if p.ID == "stale" && (p.Status != "disabled" || p.Model != "old-claude" || gotSecrets["api_key"] != "fake-claude-key" || gotSecrets["api_key_env"] != "ANTHROPIC_API_KEY") {
					t.Fatal("失效记录的供应商值、状态或主凭据被改变")
				}
				gotModels, err := store.Models(ctx, p.Target, p.ID)
				if err != nil || !(p.ID == "legacy" && mode == "overwrite") && p.ContentHash != provider.ContentHash(p, gotModels) {
					t.Fatal("清理后内容哈希未更新", err)
				}
				if p.ID == "legacy" {
					if mode == "overwrite" {
						if p.Model != "org/new-model" || gotSecrets["api_key"] != "fake-new-key" {
							t.Fatal("覆盖未导入新供应商值")
						}
					} else if p.Model != originalLegacy.Model || p.BaseURL != originalLegacy.BaseURL || p.CreatedAt != originalLegacy.CreatedAt ||
						gotSecrets["api_key"] != "fake-old-key" || gotSecrets["env:GATEWAY_HEADER"] != "fake-header" ||
						!reflect.DeepEqual(gotModels, models) || p.ContentHash == originalLegacy.ContentHash {
						t.Fatal("清理未保留旧供应商配置、凭据或模型目录")
					}
				}
			}
			if count, err := store.ImportSnapshotCount(ctx); err != nil || count != 0 {
				t.Fatal("历史快照未清空", err)
			}
			store.Close()
			render, err = os.ReadFile(renderPath)
			if err != nil || strings.Contains(string(render), "old-machine") || !strings.Contains(string(render), "org/small") {
				t.Fatal("旧渲染产物未清理或丢失模型映射", err)
			}
			actualNative, _ := os.ReadFile(nativePath)
			if !bytes.Equal(actualNative, nativeData) {
				t.Fatal("导入改写了原生配置")
			}
			out, err = command(t, root, "", args...)
			if err != nil || json.Unmarshal([]byte(out), &report) != nil || len(report.Cleaned) != 0 || report.Snapshots != 0 {
				t.Fatal("重复导入清理不幂等", err, out)
			}
		})
	}
}
