package provider

import (
	"bytes"
	"context"
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
	p := Provider{ID: "provider-a", DisplayName: "测试", Target: "codex", Source: "manual"}
	if e = s.Import(ctx, []Entry{{Provider: p, Secrets: map[string]string{"api_key": "dummy-secret-no-plaintext"}}}, "hash", []byte("mcp-dummy-token")); e != nil {
		t.Fatal(e)
	}
	sec, e := s.Secrets(ctx, p.Target, p.ID)
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

func TestStoreRoundTripsModelReasoningLevels(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	p := Provider{ID: "provider-a", DisplayName: "测试", Target: "codex", Source: "cc-switch-import", Model: "m-b"}
	entry := Entry{Provider: p, Models: []Model{
		{ProviderID: p.ID, ModelID: "m-a", DisplayName: "A", ReasoningLevels: []string{"none", "low"}, DefaultReasoningLevel: "low", SortOrder: 0},
		{ProviderID: p.ID, ModelID: "m-b", IsDefault: true, SortOrder: 1},
	}}
	if err = store.Import(ctx, []Entry{entry}, "", nil); err != nil {
		t.Fatal(err)
	}
	models, err := store.Models(ctx, p.Target, p.ID)
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
	models, err = store.Models(ctx, p.Target, p.ID)
	if err != nil || len(models[0].ReasoningLevels) != 0 || models[0].DefaultReasoningLevel != "" {
		t.Fatal("清空档位后旧值残留", err)
	}
}

func TestProviderRejectsTraversalAndCredentialURLs(t *testing.T) {
	for _, id := range []string{"../evil", "..", "a/b", `a\b`} {
		p := Provider{ID: id, DisplayName: "n", Target: "codex"}
		if p.Validate() == nil {
			t.Fatal(id)
		}
	}
	for _, url := range []string{"https://user:pass@example.test", "https://example.test?key=secret", "file:///etc/passwd"} {
		p := Provider{ID: "ok", DisplayName: "n", Target: "codex", BaseURL: url}
		if p.Validate() == nil {
			t.Fatal(url)
		}
	}
}

// 供应商身份是 (target, id)：不同 CLI 允许同 ID、同显示名称；同一 CLI 内名称唯一。
func TestStoreAllowsSameNameAcrossTargets(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	shared := "my-relay"
	for _, target := range []string{"codex", "claude"} {
		p := Provider{ID: shared, DisplayName: "My Relay", Target: target, Source: "manual"}
		if err = store.Add(ctx, p, nil); err != nil {
			t.Fatalf("不同 CLI 的同名供应商应可共存: %v", err)
		}
	}
	if err = store.Add(ctx, Provider{ID: shared, DisplayName: "My Relay", Target: "codex"}, nil); err == nil {
		t.Fatal("同一 CLI 的重复名称应被拒绝")
	}
	codex, err := store.Get(ctx, "codex", shared)
	if err != nil || codex.Target != "codex" {
		t.Fatal("按 target 读取失败", err)
	}
	for _, target := range []string{"codex", "claude"} {
		items, err := store.List(ctx, target)
		if err != nil || len(items) != 1 || items[0].Target != target {
			t.Fatalf("List(%s) 过滤失败: %+v %v", target, items, err)
		}
	}
}

// 导入路径使用 Upsert：同一 target 内，同名但不同 ID 的既有记录
// 也应被直接覆盖（用户要求「不用考虑 ID 冲突，直接覆盖」，与 cc-switch 一致）。
func TestStoreUpsertOverwritesSameNameInTarget(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	old := Provider{ID: "old-id", DisplayName: "My Relay", Target: "codex", BaseURL: "https://old.example.invalid/v1", Source: "manual"}
	if err = store.Add(ctx, old, nil); err != nil {
		t.Fatal(err)
	}
	// 新记录：ID 不同、显示名相同、base_url 不同。Upsert 应删掉 old-id 并写入 new-id。
	replacement := Entry{Provider: Provider{ID: "new-id", DisplayName: "My Relay", Target: "codex", BaseURL: "https://new.example.invalid/v1", Source: "cc-switch-import"}}
	if err = store.Upsert(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "codex", "new-id")
	if err != nil || got.BaseURL != "https://new.example.invalid/v1" || got.DisplayName != "My Relay" {
		t.Fatalf("覆盖后应读取到 new-id: %+v %v", got, err)
	}
	if _, err = store.Get(ctx, "codex", "old-id"); err == nil {
		t.Fatal("同名不同 ID 的旧记录应被覆盖移除")
	}
	items, err := store.List(ctx, "codex")
	if err != nil || len(items) != 1 || items[0].ID != "new-id" {
		t.Fatalf("覆盖后 codex 应只剩一条 new-id: %+v %v", items, err)
	}
}
