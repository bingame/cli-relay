package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/provider"
	"github.com/pelletier/go-toml/v2"
)

func sampleProvider() provider.Provider {
	return provider.Provider{ID: "test-provider", DisplayName: "测试供应商", Target: "codex", BaseURL: "https://example.invalid/v1", Model: "test-model", Source: "manual"}
}

func renderTest(t *testing.T, a *Adapter, p provider.Provider) adapter.Artifact {
	t.Helper()
	artifact, err := a.Render(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func decodeTOML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := toml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRenderAndLaunchWithoutGlobalConfig(t *testing.T) {
	a := New()
	a.NativeHome = filepath.Join(t.TempDir(), "does-not-exist")
	artifact := renderTest(t, a, sampleProvider())
	input, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": "fake-private-key", "env:EXTRA_TOKEN": "fake-extra-key", "source_settings": "must-not-be-an-env"})
	if err != nil {
		t.Fatal(err)
	}
	if input.Binary != "codex" || input.Env[artifact.EnvKey] != "" || input.Env["EXTRA_TOKEN"] != "fake-extra-key" || len(input.Env) != 1 {
		t.Fatal("回调主凭据或附加环境处理错误")
	}
	if !reflect.DeepEqual(input.UnsetEnv, []string{artifact.EnvKey}) {
		t.Fatal("未清理继承的旧凭据")
	}
	for _, secret := range []string{"fake-private-key", "fake-extra-key", "must-not-be-an-env"} {
		if strings.Contains(strings.Join(input.Args, " "), secret) || bytes.Contains(artifact.Content, []byte(secret)) {
			t.Fatal("凭据进入 argv 或配置")
		}
	}
	disk, err := os.ReadFile(artifact.Path)
	if err != nil || !bytes.Equal(disk, artifact.Content) {
		t.Fatalf("渲染文件不匹配: %v", err)
	}
	config := decodeTOML(t, artifact.Content)
	id, _ := names(artifact.ProviderID)
	if artifact.Profile["model_provider"] != id || artifact.Profile["model"] != "test-model" || config["model_provider"] != nil || config["model"] != nil {
		t.Fatal("provider 注册表与 profile 选择器未分离")
	}
	if !reflect.DeepEqual(input.Args, []string{"--profile", id}) {
		t.Fatal("未使用独立 profile")
	}
	profile, err := os.ReadFile(filepath.Join(a.NativeHome, id+".config.toml"))
	if err != nil || bytes.Contains(profile, []byte("model_providers")) {
		t.Fatal("profile 不应重复供应商注册表")
	}
	global, err := os.ReadFile(filepath.Join(a.NativeHome, "config.toml"))
	if err != nil || !bytes.Contains(global, []byte("[model_providers."+id+"]")) {
		t.Fatal("启动前未注册供应商")
	}
}

func TestNativeConfigMappingAndCredentialRejection(t *testing.T) {
	p := sampleProvider()
	p.Extra = map[string]any{"codex_config": map[string]any{
		"model_provider": "original", "model": "old", "model_reasoning_effort": "high",
		"approval_policy": "never", "shell_environment_policy": map[string]any{"inherit": "all"},
		"model_providers": map[string]any{"original": map[string]any{"name": "上游名称", "base_url": "https://old.invalid", "wire_api": "responses", "request_max_retries": int64(3), "env_key": "OLD_KEY", "query_params": map[string]any{"api-version": "2026-01"}, "env_http_headers": map[string]any{"Authorization": "EXTRA_AUTH"}}},
	}}
	// BuildLaunchInputs 会安装 profile；NativeHome 必须隔离到临时目录，
	// 否则测试会写到用户真实的 $CODEX_HOME。
	a := New()
	a.NativeHome = t.TempDir()
	artifact := renderTest(t, a, p)
	config := decodeTOML(t, artifact.Content)
	id, _ := names(p.ID)
	def := config["model_providers"].(map[string]any)[id].(map[string]any)
	if artifact.Profile["model"] != p.Model || artifact.Profile["model_reasoning_effort"] != "high" || def["base_url"] != p.BaseURL || def["auth"] == nil || def["request_max_retries"] != int64(3) {
		t.Fatal("原生字段映射错误")
	}
	if artifact.Profile["approval_policy"] != nil || config["shell_environment_policy"] != nil {
		t.Fatal("供应商配置不应扩大执行权限")
	}
	input, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Args, []string{"--profile", id}) {
		t.Fatal("未使用独立 profile")
	}
	for _, field := range []string{"api_key", "experimental_bearer_token", "http_headers", "auth", "OPENAI_API_KEY", "access_token", "clientSecret", "accessToken", "cookie"} {
		t.Run(field, func(t *testing.T) {
			bad := sampleProvider()
			bad.Extra = map[string]any{"codex_config": map[string]any{"model_providers": map[string]any{"x": map[string]any{field: "fake-secret"}}}}
			if _, err := a.Render(bad, t.TempDir()); err == nil || strings.Contains(err.Error(), "fake-secret") {
				t.Fatal("未拒绝明文凭据或错误泄露密钥")
			}
		})
	}
}

func TestCallbackBindsRelayHome(t *testing.T) {
	a := New()
	a.NativeHome = t.TempDir()
	root := t.TempDir()
	artifact, err := a.Render(sampleProvider(), root)
	if err != nil {
		t.Fatal(err)
	}
	config := decodeTOML(t, artifact.Content)
	id, _ := names(artifact.ProviderID)
	def := config["model_providers"].(map[string]any)[id].(map[string]any)
	auth, ok := def["auth"].(map[string]any)
	if !ok || auth["command"] != "relay" {
		t.Fatalf("回调配置缺失: %#v", def["auth"])
	}
	args, _ := auth["args"].([]any)
	absRoot, _ := filepath.Abs(root)
	got := make([]string, 0, len(args))
	for _, v := range args {
		got = append(got, v.(string))
	}
	want := []string{"--home", absRoot, "secret", "get", "codex", artifact.ProviderID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("回调未绑定数据目录: %q != %q", got, want)
	}
}

func TestNamesDoNotCollide(t *testing.T) {
	seenNames, seenEnv := map[string]bool{}, map[string]bool{}
	for _, id := range []string{"a-b", "a_b", "A_B", "A-b"} {
		name, env := names(id)
		if seenNames[name] || seenEnv[env] || !envName.MatchString(env) {
			t.Fatal("供应商名称或环境变量撞名")
		}
		seenNames[name], seenEnv[env] = true, true
	}
}

func TestJSONStoredIntegerConfigRemainsNativeInteger(t *testing.T) {
	p := sampleProvider()
	p.Extra = map[string]any{"codex_config": map[string]any{
		"model_provider": "original", "model_context_window": int64(200000), "model_auto_compact_token_limit": int64(180000),
		"model_providers": map[string]any{"original": map[string]any{"request_max_retries": int64(3), "stream_idle_timeout_ms": int64(120000)}},
	}}
	stored, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fromStore provider.Provider
	if err := json.Unmarshal(stored, &fromStore); err != nil {
		t.Fatal(err)
	}
	a := New()
	artifact := renderTest(t, a, fromStore)
	config := decodeTOML(t, artifact.Content)
	id, _ := names(p.ID)
	definition := config["model_providers"].(map[string]any)[id].(map[string]any)
	if artifact.Profile["model_context_window"] != int64(200000) || artifact.Profile["model_auto_compact_token_limit"] != int64(180000) || definition["request_max_retries"] != int64(3) || definition["stream_idle_timeout_ms"] != int64(120000) {
		t.Fatal("JSON往返后配置整数退化成TOML浮点数")
	}
	for _, invalid := range []any{1.5, -1.0, "3", float64(1 << 54)} {
		fromStore.Extra["codex_config"].(map[string]any)["model_context_window"] = invalid
		if _, err := a.Render(fromStore, t.TempDir()); err == nil {
			t.Fatal("未拒绝非整数或不精确数字")
		}
	}
}

func TestInvalidSecretEnvironment(t *testing.T) {
	a := New()
	artifact := renderTest(t, a, sampleProvider())
	for _, secrets := range []adapter.ResolvedSecrets{{"env:BAD=NAME": "x"}, {"env:" + artifact.EnvKey: "x"}, {"env:VALID": "x\x00y"}} {
		if _, err := a.BuildLaunchInputs(artifact, secrets); err == nil {
			t.Fatal("未拒绝非法环境变量")
		}
	}
}

func TestApplyGlobalPreservesUserConfigAndProfiles(t *testing.T) {
	a := New()
	p := sampleProvider()
	artifact := renderTest(t, a, p)
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	original := "# 用户注释\nprofile = 'old-manual' # 保留旧默认的说明\nmodel = \"user-model\" # 保留同行说明\nmodel_provider = 'user'\nnotes = '''保留多行\n# BEGIN RELAY CODEX not-real\n结束'''\n\n[model_providers.user]\nname = 'user' # 保留供应商\nbase_url = 'https://user.invalid'\n\n[projects.'C:/work']\ntrust_level = 'trusted'\n\n[profiles.historical]\nmodel = 'historical-model'\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(artifact, home); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	for _, keep := range []string{"# 用户注释", "# 保留旧默认的说明", "# 保留同行说明", "notes = '''保留多行\n# BEGIN RELAY CODEX not-real\n结束'''", "[model_providers.user]\nname = 'user' # 保留供应商", "[projects.'C:/work']\ntrust_level = 'trusted'", "[profiles.historical]\nmodel = 'historical-model'"} {
		if !bytes.Contains(first, []byte(keep)) {
			t.Fatalf("丢失用户内容: %s", keep)
		}
	}
	config := decodeTOML(t, first)
	id, _ := names(p.ID)
	if config["model"] != p.Model || config["model_provider"] != id || config["profile"] != nil {
		t.Fatal("全局指针错误")
	}
	if err := a.ApplyGlobal(artifact, home); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("重复 switch 应幂等\n第一次: %s\n第二次: %s", first, second)
	}
	p.ID = "second-provider"
	secondArtifact := renderTest(t, a, p)
	if err := a.ApplyGlobal(secondArtifact, home); err != nil {
		t.Fatal(err)
	}
	third, _ := os.ReadFile(path)
	config = decodeTOML(t, third)
	if len(config["model_providers"].(map[string]any)) != 3 {
		t.Fatal("切换时丢失其他已安装 provider")
	}
	a.LaunchMode = "profile"
	input, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil || !reflect.DeepEqual(input.Args, []string{"--profile", id}) {
		t.Fatalf("独立 profile 未安装: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, id+".config.toml"), []byte("model='user-edited'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BuildLaunchInputs(artifact, nil); err == nil {
		t.Fatal("不应使用已变更的 profile")
	}
}

func TestProfileModeInstallsWithoutChangingDefault(t *testing.T) {
	a := New()
	a.LaunchMode, a.NativeHome = "profile", t.TempDir()
	artifact := renderTest(t, a, sampleProvider())
	if _, err := a.BuildLaunchInputs(artifact, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.NativeHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	config := decodeTOML(t, data)
	if config["model_provider"] != nil || config["model"] != nil {
		t.Fatal("临时启动修改了全局默认")
	}
}

func TestProfileContainsOnlySelectorsAndCatalog(t *testing.T) {
	a := New()
	a.NativeHome = t.TempDir()
	p := sampleProvider()
	models := []provider.Model{{ProviderID: p.ID, ModelID: "test-model", DisplayName: "测试模型", IsDefault: true}}
	artifact, err := a.Render(p, t.TempDir(), models...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BuildLaunchInputs(artifact, nil); err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(filepath.Join(a.NativeHome, p.ID+".config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(profile, []byte("model_providers")) || !bytes.Contains(profile, []byte("model_catalog_json")) {
		t.Fatal("profile 内容不符合 overlay 契约")
	}
	catalog, err := os.ReadFile(filepath.Join(filepath.Dir(artifact.Path), p.ID+".catalog.json"))
	if err != nil || !bytes.Contains(catalog, []byte("test-model")) {
		t.Fatal("模型目录未生成")
	}
}

func TestApplyGlobalRejectsConflictsWithoutWriting(t *testing.T) {
	a := New()
	artifact := renderTest(t, a, sampleProvider())
	id, _ := names(artifact.ProviderID)
	for _, text := range []string{"model = ???", "[model_providers." + id + "]\nname='manual'\n", "# BEGIN RELAY CODEX " + id + "\nmodel='x'\n"} {
		t.Run(text[:5], func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.ApplyGlobal(artifact, home); err == nil {
				t.Fatal("应拒绝冲突或无效文件")
			}
			after, _ := os.ReadFile(path)
			if string(after) != text {
				t.Fatal("失败时修改了用户配置")
			}
		})
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, id+".config.toml"), []byte("model='manual'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(artifact, home); err == nil {
		t.Fatal("不应覆盖同名用户 profile")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("profile 冲突时不应创建 config.toml")
	}
}

func TestNativeArguments(t *testing.T) {
	a := New()
	cases := []struct {
		name        string
		mode        adapter.Mode
		input, want []string
	}{
		{"interactive", adapter.Interactive, []string{"hello"}, []string{"hello"}},
		{"resume-interactive", adapter.Interactive, a.ResumeArgs("session"), []string{"resume", "session"}},
		{"exec", adapter.Headless, []string{"hello"}, []string{"exec", "hello", "--json"}},
		{"resume-headless", adapter.Headless, a.ResumeArgs("session"), []string{"exec", "resume", "session", "--json"}},
		{"existing-exec", adapter.Headless, []string{"exec", "hello", "--json"}, []string{"exec", "hello", "--json"}},
		{"existing-exec-options", adapter.Headless, []string{"--model", "test", "exec", "hello"}, []string{"--model", "test", "exec", "hello", "--json"}},
		{"delimiter", adapter.Headless, []string{"--", "--json"}, []string{"exec", "--json", "--", "--json"}},
		{"multica-app-server", adapter.Headless, []string{"app-server", "--listen", "stdio://"}, []string{"app-server", "--listen", "stdio://"}},
		{"multica-app-server-options", adapter.Headless, []string{"-c", "model='test'", "app-server", "--listen", "stdio://"}, []string{"-c", "model='test'", "app-server", "--listen", "stdio://"}},
		{"existing-exec-local-provider", adapter.Headless, []string{"--local-provider", "ollama", "exec", "hello"}, []string{"--local-provider", "ollama", "exec", "hello", "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.NativeArgs(tc.mode, tc.input)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("参数错误: %v, %v", got, err)
			}
		})
	}
}

func TestProtocolBridgeDetection(t *testing.T) {
	a := New()
	for _, args := range [][]string{
		{"app-server", "--listen", "stdio://"},
		{"--config", "model='app-server'", "app-server"},
		{"--remote-auth-token-env", "DUMMY_ENV", "app-server"},
		{"--profile=test", "app-server"},
	} {
		if !a.IsProtocolBridge(args) {
			t.Fatal("未识别 app-server 原生入口")
		}
	}
	for _, args := range [][]string{
		{"--", "app-server"}, {"--model", "app-server", "hello"}, {"exec", "app-server"}, {"--profile", "app-server"},
	} {
		if a.IsProtocolBridge(args) {
			t.Fatal("把参数值或 prompt 误识别为 RPC 入口")
		}
	}
}
