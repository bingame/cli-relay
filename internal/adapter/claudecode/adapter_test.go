package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/provider"
)

func fixtureProvider() provider.Provider {
	return provider.Provider{ID: "fake-example", DisplayName: "虚构测试供应商", Targets: []string{"claude-code"}, BaseURL: "https://example.invalid/v1", Model: "fake-model", Extra: map[string]any{"claude_settings": map[string]any{"permissions": map[string]any{"allow": []string{"Read"}}, "env": map[string]any{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "fake-small"}}}}
}

func TestRenderLaunchKeepsSecretsOnlyInEnvironment(t *testing.T) {
	a := New()
	p := fixtureProvider()
	root := t.TempDir()
	artifact, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	key := "fake-test-credential-123"
	launch, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": key, "env:EXTRA_TEST_TOKEN": "fake-extra"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Binary != "claude" || launch.Env["ANTHROPIC_AUTH_TOKEN"] != key || launch.Env["EXTRA_TEST_TOKEN"] != "fake-extra" {
		t.Fatal("凭据未正确注入进程环境")
	}
	if launch.Env["ANTHROPIC_BASE_URL"] != p.BaseURL || launch.Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "fake-small" {
		t.Fatal("缺少供应商环境")
	}
	pluginPath := filepath.Join(filepath.Dir(artifact.Path), "handoff-plugin")
	if !reflect.DeepEqual(launch.Args, []string{"--setting-sources", "", "--settings", artifact.Path, "--plugin-dir", pluginPath}) {
		t.Fatalf("argv 不符合隔离契约: %q", launch.Args)
	}
	for _, relative := range []string{"SKILL.md", ".claude-plugin/plugin.json"} {
		if data, err := os.ReadFile(filepath.Join(pluginPath, relative)); err != nil || len(data) == 0 {
			t.Fatalf("缺少隔离模式下可发现的 Handoff 插件: %s: %v", relative, err)
		}
	}
	if strings.Contains(strings.Join(launch.Args, " "), key) || strings.Contains(string(artifact.Content), key) {
		t.Fatal("密钥进入 argv 或渲染产物")
	}
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(artifact.Content) {
		t.Fatal("磁盘产物不同于返回值")
	}
	if !reflect.DeepEqual(p.Extra["claude_settings"].(map[string]any)["env"], map[string]any{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "fake-small"}) {
		t.Fatal("渲染修改了原始供应商")
	}
	for _, expected := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_USE_BEDROCK"} {
		found := false
		for _, name := range launch.UnsetEnv {
			found = found || name == expected
		}
		if !found {
			t.Errorf("未清除继承变量 %s", expected)
		}
	}
}

func TestRenderRejectsSecretFieldsAndInvalidPaths(t *testing.T) {
	for _, field := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OAUTH_TOKEN", "apiKey", "client_secret"} {
		t.Run(field, func(t *testing.T) {
			p := fixtureProvider()
			p.Extra["claude_settings"] = map[string]any{"env": map[string]any{field: "fake-secret-never-echo"}}
			_, err := New().Render(p, t.TempDir())
			if err == nil {
				t.Fatal("未拒绝明文凭据")
			}
			if strings.Contains(err.Error(), "fake-secret-never-echo") {
				t.Fatal("错误泄露凭据")
			}
		})
	}
	p := fixtureProvider()
	p.ID = "../escape"
	if _, err := New().Render(p, t.TempDir()); err == nil {
		t.Fatal("未拒绝路径穿越")
	}
}

func TestBuildRejectsInvalidEnvironmentAndDualAuthentication(t *testing.T) {
	a := New()
	artifact, err := a.Render(fixtureProvider(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, secrets := range []adapter.ResolvedSecrets{{"api_key": "fake-a", "env:ANTHROPIC_API_KEY": "fake-b"}, {"api_key": "fake\nkey"}, {"env:INVALID=NAME": "fake"}} {
		if _, err := a.BuildLaunchInputs(artifact, secrets); err == nil {
			t.Fatal("应拒绝无效/冲突环境")
		}
	}
}

func TestNativeArgs(t *testing.T) {
	a := New()
	tests := []struct {
		name string
		args []string
		want []string
		fail bool
	}{
		{"prompt", []string{"继续任务"}, []string{"-p", "--verbose", "--output-format", "stream-json", "继续任务"}, false},
		{"duplicate", []string{"--print", "--verbose", "--output-format=stream-json", "--resume", "session-fake", "继续任务"}, []string{"-p", "--verbose", "--output-format", "stream-json", "--resume", "session-fake", "继续任务"}, false},
		{"conflicting-output", []string{"-p", "--output-format", "json"}, nil, true},
		{"missing-output", []string{"--output-format"}, nil, true},
		{"settings-override", []string{"--settings=other.json"}, nil, true},
		{"sources-override", []string{"--setting-sources", "user"}, nil, true},
		{"literal-prompt", []string{"--", "--settings"}, []string{"-p", "--verbose", "--output-format", "stream-json", "--", "--settings"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := a.NativeArgs(adapter.Headless, test.args)
			if (err != nil) != test.fail {
				t.Fatalf("意外错误 %v", err)
			}
			if err == nil && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("得到 %q，需要 %q", got, test.want)
			}
		})
	}
	args := []string{"--resume", "fake-session", "继续"}
	got, err := a.NativeArgs(adapter.Interactive, args)
	if err != nil || !reflect.DeepEqual(got, args) {
		t.Fatal("交互参数未原样保留")
	}
	if !reflect.DeepEqual(a.ResumeArgs("fake-session"), []string{"--resume", "fake-session"}) {
		t.Fatal("恢复参数错误")
	}
}

func readSettings(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeSettings(t *testing.T, home string, value map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyGlobalPreservesUnrelatedAndCleansPriorProvider(t *testing.T) {
	a := New()
	home := t.TempDir()
	root := t.TempDir()
	writeSettings(t, home, map[string]any{"theme": "dark", "env": map[string]any{"USER_CUSTOM": "retained"}})
	p := fixtureProvider()
	first, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(first, home); err != nil {
		t.Fatal(err)
	}
	p.ID = "second"
	p.Model = "second-model"
	p.Extra = nil
	next, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(next, home); err != nil {
		t.Fatal(err)
	}
	settings := readSettings(t, home)
	env := settings["env"].(map[string]any)
	if settings["theme"] != "dark" || env["USER_CUSTOM"] != "retained" || settings["model"] != "second-model" {
		t.Fatal("未正确合并设置")
	}
	if _, ok := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; ok {
		t.Fatal("残留上一供应商模型")
	}
	if _, ok := settings["permissions"]; ok {
		t.Fatal("残留上一供应商权限设置")
	}
	if err := a.ApplyGlobal(next, home); err != nil {
		t.Fatal("重复切换应幂等", err)
	}
}

func TestApplyGlobalProtectsManualEdits(t *testing.T) {
	a := New()
	home := t.TempDir()
	root := t.TempDir()
	p := fixtureProvider()
	first, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(first, home); err != nil {
		t.Fatal(err)
	}
	settings := readSettings(t, home)
	settings["model"] = "manual-model"
	writeSettings(t, home, settings)
	before, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	p.ID = "second"
	p.Model = "second-model"
	p.Extra = nil
	next, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(next, home); err == nil {
		t.Fatal("应报告手工修改冲突")
	}
	after, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	if string(before) != string(after) {
		t.Fatal("冲突时修改了配置")
	}
}

func TestApplyGlobalPreservesModifiedObsoleteField(t *testing.T) {
	a := New()
	home := t.TempDir()
	root := t.TempDir()
	p := fixtureProvider()
	first, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(first, home); err != nil {
		t.Fatal(err)
	}
	settings := readSettings(t, home)
	settings["permissions"].(map[string]any)["allow"] = []string{"Read", "Write"}
	writeSettings(t, home, settings)
	p.ID = "second"
	p.Extra = nil
	next, err := a.Render(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(next, home); err != nil {
		t.Fatal(err)
	}
	settings = readSettings(t, home)
	if !reflect.DeepEqual(settings["permissions"].(map[string]any)["allow"], []any{"Read", "Write"}) {
		t.Fatal("删除了用户修改的旧字段")
	}
}

func TestApplyGlobalRejectsExistingConflictAndInvalidJSON(t *testing.T) {
	a := New()
	artifact, err := a.Render(fixtureProvider(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{`{"model":"manual"}`, `{"broken":`} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.ApplyGlobal(artifact, home); err == nil {
			t.Fatal("应保护已有冲突或损坏配置")
		}
		data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
		if string(data) != content {
			t.Fatal("失败时覆盖已有配置")
		}
	}
}
