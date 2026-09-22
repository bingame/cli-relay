package provider

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bingame/cli-relay/internal/secrets"
)

func TestStoreEncryptedImportAndAtomicConflict(t *testing.T) {
	root := t.TempDir()
	v, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	s, e := Open(root, v)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	p := Provider{ID: "provider-a", DisplayName: "测试", Targets: []string{"codex"}, Source: "manual"}
	if e = s.Import(ctx, []Entry{{Provider: p, Secrets: map[string]string{"api_key": "dummy-secret-no-plaintext"}}}, "hash", []byte("mcp-dummy-token")); e != nil {
		t.Fatal(e)
	}
	sec, e := s.Secrets(ctx, p.ID)
	if e != nil || sec["api_key"] != "dummy-secret-no-plaintext" {
		t.Fatal(e)
	}
	next := p
	next.ID = "new"
	if e = s.Import(ctx, []Entry{{Provider: next}, {Provider: p}}, "", nil); e == nil {
		t.Fatal("重复ID被覆盖")
	}
	items, e := s.List(ctx, "")
	if e != nil || len(items) != 1 {
		t.Fatal("事务未回滚", e)
	}
	s.Close()
	data, _ := os.ReadFile(filepath.Join(root, "providers.db"))
	for _, secret := range []string{"dummy-secret-no-plaintext", "mcp-dummy-token"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("明文落盘")
		}
	}
	ro, e := OpenReadOnly(root)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	items, e = ro.List(ctx, "codex")
	if e != nil || len(items) != 1 {
		t.Fatal("只读读取失败", e)
	}
}

func TestOpenMigratesLegacySchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "providers.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE providers (id TEXT PRIMARY KEY,display_name TEXT NOT NULL,targets TEXT NOT NULL,base_url TEXT,model TEXT,extra_json TEXT,source TEXT,created_at TEXT,updated_at TEXT); CREATE TABLE import_log(id INTEGER PRIMARY KEY,source TEXT,file_hash TEXT,imported_at TEXT,raw_snapshot BLOB);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Add(context.Background(), Provider{ID: "legacy", DisplayName: "旧供应商", Targets: []string{"codex"}}, nil); err != nil {
		t.Fatal(err)
	}
	p, err := store.Get(context.Background(), "legacy")
	if err != nil || p.EffectiveStatus() != "active" || p.EffectiveSecretMode() != "callback" {
		t.Fatal("旧库迁移失败", err)
	}
}

// 老库的 provider_models 没有思考档位列；迁移必须补列并把旧行读成"未声明"，
// 不能让 NULL 把 Models 的扫描打断。
func TestOpenMigratesLegacyModelColumns(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE providers (id TEXT PRIMARY KEY,display_name TEXT NOT NULL,targets TEXT NOT NULL,base_url TEXT,model TEXT,extra_json TEXT,source TEXT,created_at TEXT,updated_at TEXT,secret_mode TEXT NOT NULL DEFAULT 'callback',status TEXT NOT NULL DEFAULT 'active',content_hash TEXT);
CREATE TABLE provider_models(provider_id TEXT NOT NULL,model_id TEXT NOT NULL,display_name TEXT,context_window INTEGER,is_default INTEGER NOT NULL DEFAULT 0,sort_order INTEGER,PRIMARY KEY(provider_id,model_id));
INSERT INTO providers(id,display_name,targets,extra_json,source) VALUES('legacy','旧供应商','["codex"]','{}','cc-switch-import');
INSERT INTO provider_models(provider_id,model_id,display_name,context_window,is_default,sort_order) VALUES('legacy','old-model','旧模型',128000,1,0);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	models, err := store.Models(context.Background(), "legacy")
	if err != nil {
		t.Fatal("旧库迁移后模型目录读取失败", err)
	}
	if len(models) != 1 || models[0].ModelID != "old-model" || models[0].ContextWindow != 128000 || len(models[0].ReasoningLevels) != 0 || models[0].DefaultReasoningLevel != "" {
		t.Fatalf("旧条目未按未声明档位读回: %+v", models)
	}
}

func TestStoreRoundTripsModelReasoningLevels(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	p := Provider{ID: "provider-a", DisplayName: "测试", Targets: []string{"codex"}, Source: "cc-switch-import", Model: "m-b"}
	entry := Entry{Provider: p, Models: []Model{
		{ProviderID: p.ID, ModelID: "m-a", DisplayName: "A", ReasoningLevels: []string{"none", "low"}, DefaultReasoningLevel: "low", SortOrder: 0},
		{ProviderID: p.ID, ModelID: "m-b", IsDefault: true, SortOrder: 1},
	}}
	if err = store.Import(ctx, []Entry{entry}, "", nil); err != nil {
		t.Fatal(err)
	}
	models, err := store.Models(ctx, p.ID)
	if err != nil || len(models) != 2 {
		t.Fatal("模型目录写入失败", err)
	}
	if models[0].ModelID != "m-a" || !reflect.DeepEqual(models[0].ReasoningLevels, []string{"none", "low"}) || models[0].DefaultReasoningLevel != "low" {
		t.Fatalf("思考档位未往返: %+v", models[0])
	}
	if len(models[1].ReasoningLevels) != 0 || models[1].DefaultReasoningLevel != "" {
		t.Fatalf("未声明档位的条目被写入了档位: %+v", models[1])
	}
	entry.Models[0].ReasoningLevels = nil
	entry.Models[0].DefaultReasoningLevel = ""
	if err = store.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	models, err = store.Models(ctx, p.ID)
	if err != nil || len(models[0].ReasoningLevels) != 0 || models[0].DefaultReasoningLevel != "" {
		t.Fatal("清空档位后旧值残留", err)
	}
}

func TestProviderRejectsTraversalAndCredentialURLs(t *testing.T) {
	for _, id := range []string{"../evil", "..", "a/b", `a\b`} {
		p := Provider{ID: id, DisplayName: "n", Targets: []string{"codex"}}
		if p.Validate() == nil {
			t.Fatal(id)
		}
	}
	for _, url := range []string{"https://user:pass@example.test", "https://example.test?key=secret", "file:///etc/passwd"} {
		p := Provider{ID: "ok", DisplayName: "n", Targets: []string{"codex"}, BaseURL: url}
		if p.Validate() == nil {
			t.Fatal(url)
		}
	}
}
