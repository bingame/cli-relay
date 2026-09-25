package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/adapter/mock"
	"github.com/spf13/cobra"
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
func TestProviderResolutionByDisplayName(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	native := t.TempDir()
	t.Setenv("CODEX_HOME", native)
	add := func(id, name, target string) {
		t.Helper()
		args := []string{"provider", "add", "--id", id, "--target", target, "--base-url", "https://example.test/v1"}
		if name != "" {
			args = append(args, "--name", name)
		}
		if _, e := command(t, root, "dummy-secret-"+id, append(args, "--api-key-stdin")...); e != nil {
			t.Fatal(e)
		}
	}
	add("one", "友好渠道", "codex")
	add("two", "dup", "codex")
	add("three", "dup", "claude")
	if _, e := command(t, root, "", "switch", "友好渠道", "--target", "codex"); e != nil {
		t.Fatal("显示名称 switch 失败", e)
	}
	state, e := command(t, root, "", "status")
	if e != nil || !strings.Contains(state, `"codex": "one"`) {
		t.Fatal(state, e)
	}
	if _, e = command(t, root, "", "provider", "render-args", "codex", "友好渠道"); e != nil {
		t.Fatal("显示名称 render-args 失败", e)
	}
	if _, e = command(t, root, "", "switch", "dup"); e == nil || !strings.Contains(e.Error(), "three") || !strings.Contains(e.Error(), "two") {
		t.Fatal("重名应报错并列出候选 ID", e)
	}
	if _, e = command(t, root, "", "provider", "remove", "友好渠道"); e == nil || !strings.Contains(e.Error(), "全局默认引用") {
		t.Fatal("激活供应商应拒绝删除", e)
	}
	if _, e = command(t, root, "", "provider", "remove", "不存在的供应商"); e == nil || !strings.Contains(e.Error(), "未找到供应商") {
		t.Fatal("不存在应报未找到", e)
	}
	if _, e = command(t, root, "", "provider", "remove", "three", "--target", "claude"); e != nil {
		t.Fatal("按 ID 删除失败", e)
	}
}

func TestProviderCompletionCandidates(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	t.Setenv("CODEX_HOME", t.TempDir())
	add := func(id, name, target string) {
		t.Helper()
		args := []string{"provider", "add", "--id", id, "--target", target, "--base-url", "https://example.test/v1", "--api-key-stdin"}
		if name != "" {
			args = append(args, "--name", name)
		}
		if _, e := command(t, root, "dummy-secret-"+id, args...); e != nil {
			t.Fatal(e)
		}
	}
	add("one", "友好渠道", "codex")
	add("two", "spaced name", "codex")
	add("three", "claude-only", "claude")
	app := &App{Home: root}
	cmd := NewRootWithApp(app)
	cmd.SetContext(context.Background())
	out, directive := app.completeProviders("codex")(cmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatal("补全应禁止文件补全")
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "one\t友好渠道") || !strings.Contains(joined, "友好渠道\tone") {
		t.Fatal("缺少 ID 与显示名称候选", out)
	}
	if !strings.Contains(joined, "two\tspaced name") || strings.Contains(joined, "spaced name\ttwo") {
		t.Fatal("含空白的显示名称不应成为候选", out)
	}
	if strings.Contains(joined, "three") {
		t.Fatal("target 过滤失效", out)
	}
	out, _ = app.completeProviderArg()(cmd, []string{"codex"}, "")
	if strings.Join(out, "\n") == "" {
		t.Fatal("位置参数补全为空")
	}
	out, _ = app.completeProviders("")(cmd, nil, "")
	if !strings.Contains(strings.Join(out, "\n"), "three") {
		t.Fatal("不过滤 target 时应包含全部供应商", out)
	}
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
	if !bytes.Contains(before, []byte(original)) || !bytes.Contains(before, []byte("[model_providers.one]")) {
		t.Fatal("临时启动应只注册 provider 并保留原配置")
	}
	env, e := command(t, root, "", "provider", "render-env", "codex", "one", "--format", "json")
	if e != nil {
		t.Fatal(e)
	}
	var payload struct {
		Env   map[string]string `json:"env"`
		Unset []string          `json:"unset"`
	}
	if json.Unmarshal([]byte(env), &payload) != nil {
		t.Fatal("Multica env必须JSON")
	}
	if len(payload.Env) != 0 {
		t.Fatal("callback 模式的 render-env.env 应为空")
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
func TestImportDryRunAndDefaultOverwrite(t *testing.T) {
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
	if strings.Contains(out, "-imported-1") {
		t.Fatal("默认应按显示名覆盖同步，而不是重复改名", out)
	}
	if _, e = os.Stat(filepath.Join(root, "current.json")); !os.IsNotExist(e) {
		t.Fatal("导入自动激活了供应商")
	}
}

func TestImportRejectsSlugAmbiguity(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	// 同一 CLI 下 "A B" 与 "A-B" 归一化为同一 ID a-b；两条都导入时必须拒绝后一条，不能静默合并。
	sql := strings.Join([]string{
		"-- CC Switch SQLite 导出",
		"CREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, meta TEXT, is_current INTEGER);",
		"INSERT INTO providers VALUES ('one', 'claude', 'A B', '{}', NULL, 0);",
		"INSERT INTO providers VALUES ('two', 'claude', 'A-B', '{}', NULL, 0);",
	}, "\n")
	fixture := filepath.Join(t.TempDir(), "slug.sql")
	if err := os.WriteFile(fixture, []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := command(t, root, "", "provider", "import", "--from", "cc-switch", fixture)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	kept, rejected := 0, 0
	for _, row := range report.Providers {
		if row["id"] == "a-b" && row["rejected"] == true {
			rejected++
		} else if row["id"] == "a-b" {
			kept++
		}
	}
	if kept != 1 || rejected != 1 {
		t.Fatalf("slug 歧义未拒绝: %#v", report.Providers)
	}
	listed, err := command(t, root, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	var providers []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal([]byte(listed), &providers); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range providers {
		if p.ID == "a-b" {
			count++
			if p.DisplayName != "A B" {
				t.Fatalf("误合并为后一条名称: %#v", providers)
			}
		}
	}
	if count != 1 {
		t.Fatalf("数据库中应只剩一条 a-b: %#v", providers)
	}
}

func TestImportSkipPreservesSameNameDifferentID(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	// 手工创建 custom-id / Same Name，导入同名的 cc-switch 记录时 skip 应保留旧记录。
	if _, err := command(t, root, "dummy-secret", "provider", "add", "--id", "custom-id", "--name", "Same Name", "--target", "claude", "--api-key-stdin"); err != nil {
		t.Fatal(err)
	}
	sql := strings.Join([]string{
		"-- CC Switch SQLite 导出",
		"CREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, meta TEXT, is_current INTEGER);",
		"INSERT INTO providers VALUES ('same-name', 'claude', 'Same Name', '{}', NULL, 0);",
	}, "\n")
	fixture := filepath.Join(t.TempDir(), "same.sql")
	if err := os.WriteFile(fixture, []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := command(t, root, "", "provider", "import", "--from", "cc-switch", fixture, "--on-conflict", "skip")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"skipped": true`) {
		t.Fatalf("skip 未报告同名不同 ID 冲突: %s", out)
	}
	listed, err := command(t, root, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, `"custom-id"`) || strings.Contains(listed, `"same-name"`) {
		t.Fatalf("skip 应保留旧记录: %s", listed)
	}
}

func TestRenderEnvCarriesUnset(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	if _, err := command(t, root, "dummy-secret", "provider", "add", "--id", "claude-one", "--target", "claude", "--api-key-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := command(t, root, "", "provider", "render-env", "claude", "claude-one", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Env   map[string]string `json:"env"`
		Unset []string          `json:"unset"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal("render-env json 必须是对象")
	}
	if len(payload.Env) != 0 {
		t.Fatalf("claude callback 模式 env 应为空: %#v", payload.Env)
	}
	if !strings.Contains(strings.Join(payload.Unset, " "), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("render-env 未携带删除语义: %#v", payload.Unset)
	}
	dotenv, err := command(t, root, "", "provider", "render-env", "claude", "claude-one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dotenv, "RELAY_UNSET_ENV=ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("dotenv 输出缺少删除语义: %s", dotenv)
	}
}

func TestPrepareReinjectsPassphraseForCallback(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	ad := &mock.Adapter{Name: "claude", Inputs: adapter.LaunchInputs{Binary: "fake"}}
	app := &App{Home: root, Adapters: map[string]adapter.LaunchAdapter{"claude": ad}}
	cmd := NewRootWithApp(app)
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"provider", "add", "--id", "cb", "--target", "claude", "--api-key-stdin"})
	cmd.SetIn(strings.NewReader("dummy-secret"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	opts, err := app.prepare(cmd, "claude", "cb", adapter.Headless, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Inputs.Env["RELAY_PASSPHRASE"] != "test-passphrase-long-enough" {
		t.Fatalf("RELAY_PASSPHRASE 未透传给回调启动: %#v", opts.Inputs.Env)
	}
	found := false
	for _, v := range opts.SecretValues {
		found = found || v == "test-passphrase-long-enough"
	}
	if !found {
		t.Fatal("口令未加入脱敏列表")
	}
}

func TestImportManualCollisionOverwrites(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	if _, err := command(t, root, "", "provider", "add", "--id", "codex", "--name", "Codex 演示", "--target", "codex"); err != nil {
		t.Fatal(err)
	}

	fixture := filepath.Join("..", "..", "testdata", "ccswitch", "sample.sql")
	out, err := command(t, root, "", "provider", "import", "--from", "cc-switch", fixture)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Providers []map[string]any `json:"providers"`
	}
	if err = json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range report.Providers {
		if row["display_name"] != "Codex 演示" {
			continue
		}
		found = true
		if row["id"] != "codex" || row["target"] != "codex" || row["original_id"] != "demo" {
			t.Fatalf("manual 撞号报告不符合直接覆盖约定: %#v", row)
		}
		if _, exists := row["conflict_renamed"]; exists {
			t.Fatalf("覆盖语义下不应报告改名: %#v", row)
		}
	}
	if !found {
		t.Fatal("导入报告缺少 Codex 演示记录")
	}

	listed, err := command(t, root, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	var providers []struct {
		ID     string `json:"id"`
		Source string `json:"source"`
	}
	if err = json.Unmarshal([]byte(listed), &providers); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, item := range providers {
		sources[item.ID] = item.Source
	}
	if len(sources) != 3 || sources["codex"] != "cc-switch-import" || sources["claude-attach"] != "cc-switch-import" {
		t.Fatalf("manual 记录未被导入记录直接覆盖: %#v", sources)
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

func TestSecretGetWritesOnlyCredential(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	if _, err := command(t, root, "fake-callback-key", "provider", "add", "--id", "callback", "--target", "codex", "--api-key-stdin"); err != nil {
		t.Fatal(err)
	}
	out, err := command(t, root, "", "secret", "get", "codex", "callback")
	if err != nil {
		t.Fatal(err)
	}
	if out != "fake-callback-key" {
		t.Fatalf("stdout 混入了非密钥内容: %q", out)
	}
}

func TestDisabledProviderCannotSwitchOrLaunch(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "ccswitch", "sample.sql")
	if _, err := command(t, root, "", "provider", "import", "--from", "cc-switch", fixture); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "empty.sql")
	data := "-- CC Switch SQLite 导出\nCREATE TABLE providers (id TEXT, app_type TEXT, name TEXT, settings_config TEXT, meta TEXT, is_current INTEGER);"
	if err := os.WriteFile(empty, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t, root, "", "provider", "import", "--from", "cc-switch", empty, "--prune"); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t, root, "", "switch", "codex", "--target", "codex"); err == nil || !strings.Contains(err.Error(), "失效") {
		t.Fatal("disabled provider 仍可切换")
	}
}
