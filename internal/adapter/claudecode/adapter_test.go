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
	return provider.Provider{ID: "fake-example", DisplayName: "虚构测试供应商", Target: "claude", BaseURL: "https://example.invalid/v1", Model: "fake-model", Extra: map[string]any{"claude_settings": map[string]any{"permissions": map[string]any{"allow": []string{"Read"}}, "env": map[string]any{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "fake-small"}}}}
}

func TestRenderLaunchUsesAPIKeyHelper(t *testing.T) {
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
	if launch.Binary != "claude" || len(launch.Env) != 0 {
		t.Fatal("回调模式不应向进程环境注入凭据")
	}
	var settings map[string]any
	if json.Unmarshal(artifact.Content, &settings) != nil || settings["apiKeyHelper"] != "relay secret get claude "+p.ID {
		t.Fatal("未生成 apiKeyHelper")
	}
	pluginPath := filepath.Join(filepath.Dir(artifact.Path), "handoff-plugin")
	if !reflect.DeepEqual(launch.Args, []string{"--setting-sources", "", "--plugin-dir", pluginPath, "--settings", artifact.Path}) {
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

func TestRenderConsumesOnlyWhitelistedSettings(t *testing.T) {
	a := New()
	p := fixtureProvider()
	p.Extra["claude_settings"] = map[string]any{
		"permissions": map[string]any{"allow": []string{"Read"}},
		"hooks":       map[string]any{"PreToolUse": []any{map[string]any{"type": "command", "command": "echo hi"}}},
		"statusLine":  map[string]any{"type": "command", "command": "echo status"},
		"env":         map[string]any{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "fake-small"},
	}
	artifact, err := a.Render(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if json.Unmarshal(artifact.Content, &settings) != nil {
		t.Fatal("渲染产物不是合法 JSON")
	}
	for _, key := range []string{"permissions", "hooks", "statusLine"} {
		if _, ok := settings[key]; ok {
			t.Fatalf("spec §6 白名单：%s 不应进入渲染产物", key)
		}
	}
	if settings["env"].(map[string]any)["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "fake-small" {
		t.Fatal("env 语义字段未保留")
	}
}

func TestRenderStripsInheritedClaudeCredentials(t *testing.T) {
	a := New()
	p := fixtureProvider()
	p.Extra["claude_settings"] = map[string]any{"env": map[string]any{
		"ANTHROPIC_AUTH_TOKEN": "fake-auth-token",
		"ANTHROPIC_API_KEY":    "fake-api-key",
		"SAFE_FLAG":            "yes",
	}}
	artifact, err := a.Render(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if json.Unmarshal(artifact.Content, &settings) != nil {
		t.Fatal("渲染产物不是合法 JSON")
	}
	env := settings["env"].(map[string]any)
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, ok := env[key]; ok {
			t.Fatalf("渲染产物不应包含凭据字段 %s", key)
		}
	}
	if env["SAFE_FLAG"] != "yes" || settings["apiKeyHelper"] != "relay secret get claude "+p.ID {
		t.Fatal("未保留非凭据设置或未生成 Relay apiKeyHelper")
	}
	launch, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		found := false
		for _, name := range launch.UnsetEnv {
			found = found || name == expected
		}
		if !found {
			t.Fatalf("启动环境未清除 %s", expected)
		}
	}
}

func TestRenderRejectsSecretFieldsAndInvalidPaths(t *testing.T) {
	for _, field := range []string{"OAUTH_TOKEN", "apiKey", "client_secret"} {
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

func TestBuildIgnoresResolvedSecretsInCallbackMode(t *testing.T) {
	a := New()
	artifact, err := a.Render(fixtureProvider(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": "fake\nkey", "env:INVALID=NAME": "fake"})
	if err != nil || len(launch.Env) != 0 {
		t.Fatal("回调模式不应消费已解析密钥")
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
	settings["env"].(map[string]any)["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "user-changed-model"
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
	if settings["env"].(map[string]any)["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "user-changed-model" {
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
