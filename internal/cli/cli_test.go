package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relay/internal/adapter"
	"relay/internal/adapter/mock"
)

func command(t *testing.T, home, input string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootWithApp(&App{Home: home})
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	e := cmd.Execute()
	return out.String(), e
}
func TestProviderAddRenderSwitchAndIsolation(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	native := t.TempDir()
	t.Setenv("CODEX_HOME", native)
	original := "# 手写注释\nmodel = 'old'\n[custom]\nvalue = 'keep'\n"
	if e := os.WriteFile(filepath.Join(native, "config.toml"), []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"one", "two"} {
		_, e := command(t, root, "dummy-secret-"+id, "provider", "add", "--id", id, "--target", "codex", "--base-url", "https://example.test/v1", "--model", "test-model", "--api-key-stdin")
		if e != nil {
			t.Fatal(e)
		}
	}
	args, e := command(t, root, "", "provider", "render-args", "codex", "one")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(args, "dummy-secret") {
		t.Fatal("argv泄漏密钥")
	}
	before, _ := os.ReadFile(filepath.Join(native, "config.toml"))
	if string(before) != original {
		t.Fatal("临时渲染修改全局配置")
	}
	env, e := command(t, root, "", "provider", "render-env", "codex", "one", "--format", "json")
	if e != nil {
		t.Fatal(e)
	}
	var values map[string]string
	if json.Unmarshal([]byte(env), &values) != nil {
		t.Fatal("Multica env必须JSON")
	}
	found := false
	for _, v := range values {
		found = found || v == "dummy-secret-one"
	}
	if !found {
		t.Fatal("未输出密钥")
	}
	if _, e = command(t, root, "", "switch", "one", "--target", "codex"); e != nil {
		t.Fatal(e)
	}
	_, e = command(t, root, "", "provider", "render-args", "codex", "two")
	if e != nil {
		t.Fatal(e)
	}
	state, e := command(t, root, "", "status")
	if e != nil || !strings.Contains(state, `"codex": "one"`) {
		t.Fatal(state, e)
	}
	after, _ := os.ReadFile(filepath.Join(native, "config.toml"))
	if !bytes.Contains(after, []byte("# 手写注释")) || !bytes.Contains(after, []byte("'keep'")) {
		t.Fatal("丢失用户配置")
	}
	if _, e = command(t, root, "", "provider", "remove", "one"); e == nil {
		t.Fatal("删除了激活供应商")
	}
}
func TestImportDryRunNoStateAndConflictRename(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := filepath.Join(t.TempDir(), "nonexistent")
	fixture := filepath.Join("..", "..", "testdata", "ccswitch", "sample.sql")
	out, e := command(t, root, "", "provider", "import", "--from", "cc-switch", fixture, "--dry-run")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out, `"dry_run": true`) {
		t.Fatal(out)
	}
	if _, e = os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("dry-run写入数据目录")
	}
	for i := 0; i < 2; i++ {
		out, e = command(t, root, "", "provider", "import", "--from", "cc-switch", fixture)
		if e != nil {
			t.Fatal(e)
		}
	}
	if !strings.Contains(out, "-imported-1") {
		t.Fatal("未显式报告冲突", out)
	}
	if _, e = os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(e) {
		t.Fatal("导入自动激活了供应商")
	}
}
func TestCLIUsesMockAdapterContract(t *testing.T) {
	root := t.TempDir()
	ad := &mock.Adapter{Name: "test", Inputs: adapter.LaunchInputs{Binary: "fake", Args: []string{"--native"}}}
	app := &App{Home: root, Adapters: map[string]adapter.LaunchAdapter{"test": ad}}
	for _, args := range [][]string{{"provider", "add", "--id", "one", "--target", "test"}, {"switch", "one", "--target", "test"}} {
		cmd := NewRootWithApp(app)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetArgs(args)
		if e := cmd.Execute(); e != nil {
			t.Fatal(e)
		}
	}
	if !ad.Applied {
		t.Fatal("未调用Adapter.ApplyGlobal")
	}
}
