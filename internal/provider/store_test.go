package provider

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
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
