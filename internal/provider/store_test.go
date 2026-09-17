package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"relay/internal/secrets"
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
	if e = s.Import(ctx, []Entry{{p, map[string]string{"api_key": "dummy-secret-no-plaintext"}}}, "hash", []byte("mcp-dummy-token")); e != nil {
		t.Fatal(e)
	}
	sec, e := s.Secrets(ctx, p.ID)
	if e != nil || sec["api_key"] != "dummy-secret-no-plaintext" {
		t.Fatal(e)
	}
	next := p
	next.ID = "new"
	if e = s.Import(ctx, []Entry{{next, nil}, {p, nil}}, "", nil); e == nil {
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
