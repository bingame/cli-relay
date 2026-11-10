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

func checkLaunchArgs(t *testing.T, input adapter.LaunchInputs, artifact adapter.Artifact) {
	t.Helper()
	if len(input.Args) != 0 || input.Env["CODEX_HOME"] == "" || input.Env["CODEX_SQLITE_HOME"] == "" {
		t.Fatalf("未返回隔离配置目录与共享 SQLite 目录: %q", input.Args)
	}
	data, err := os.ReadFile(filepath.Join(input.Env["CODEX_HOME"], "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeTOML(t, data)["model_providers"], decodeTOML(t, artifact.Content)["model_providers"]) {
		t.Fatal("隔离配置与供应商定义不一致")
	}
}

func TestRenderAndLaunchWithoutGlobalConfig(t *testing.T) {
	a := New()
	a.NativeHome = filepath.Join(t.TempDir(), "does-not-exist")
	artifact := renderTest(t, a, sampleProvider())
	input, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": "fake-private-key", "env:EXTRA_TOKEN": "fake-extra-key", "source_settings": "must-not-be-an-env"})
	if err != nil {
		t.Fatal(err)
	}
	if input.Binary != "codex" || input.Env[artifact.EnvKey] != "" || input.Env["EXTRA_TOKEN"] != "fake-extra-key" || len(input.Env) != 3 {
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
	nativeID, _ := names(artifact.ProviderID)
	if artifact.Profile["model_provider"] != nativeID || artifact.Profile["model"] != "test-model" || config["model_provider"] != nil || config["model"] != nil {
		t.Fatal("provider 注册表与 profile 选择器未分离")
	}
	checkLaunchArgs(t, input, artifact)
	profile, err := os.ReadFile(filepath.Join(input.Env["CODEX_HOME"], "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if decodeTOML(t, profile)["model_providers"].(map[string]any)[nativeID] == nil {
		t.Fatal("隔离配置缺少完整供应商表")
	}
	if _, err := os.Stat(filepath.Join(a.NativeHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("临时启动不应创建或修改主配置")
	}
}

func TestAppServerLaunchWithoutProfiles(t *testing.T) {
	for _, secretMode := range []string{"callback", "env_key"} {
		t.Run(secretMode, func(t *testing.T) {
			a := New()
			a.NativeHome = filepath.Join(t.TempDir(), "untouched")
			p := sampleProvider()
			p.SecretMode = secretMode
			p.Extra = map[string]any{"codex_config": map[string]any{"model_reasoning_effort": "xhigh"}}
			artifact := renderTest(t, a, p)
			inputs, err := a.BuildAppServerInputs(artifact, adapter.ResolvedSecrets{"api_key": "fake-app-server-key"})
			if err != nil {
				t.Fatal(err)
			}
			config := map[string]any{}
			for i := 0; i < len(inputs.Args); i += 2 {
				if inputs.Args[i] != "-c" || i+1 >= len(inputs.Args) {
					t.Fatalf("app-server 参数必须仅包含 -c 覆盖: %q", inputs.Args)
				}
				for key, value := range decodeTOML(t, []byte(inputs.Args[i+1])) {
					config[key] = value
				}
			}
			id := "relay_" + p.ID
			if config["model_provider"] != id || config["model"] != p.Model || config["model_reasoning_effort"] != "xhigh" {
				t.Fatal("app-server 未渲染供应商选择器或模型设置", config)
			}
			defs := config["model_providers"].(map[string]any)
			definition := decodeTOML(t, artifact.Content)["model_providers"].(map[string]any)[nativeProviderID]
			if len(defs) != 1 || !reflect.DeepEqual(defs[id], definition) {
				t.Fatal("app-server 未使用独立命名空间中的完整供应商表")
			}
			if strings.Contains(strings.Join(inputs.Args, " "), "fake-app-server-key") {
				t.Fatal("app-server 参数泄露凭据")
			}
			if (inputs.Env[artifact.EnvKey] != "") != (secretMode == "env_key") {
				t.Fatal("app-server 凭据模式处理错误")
			}
			if _, err := os.Stat(a.NativeHome); !os.IsNotExist(err) {
				t.Fatal("app-server 渲染不应创建原生配置或 profile")
			}
		})
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
	nativeID, _ := names(p.ID)
	def := config["model_providers"].(map[string]any)[nativeID].(map[string]any)
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
	checkLaunchArgs(t, input, artifact)
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
	nativeID, _ := names(artifact.ProviderID)
	def := config["model_providers"].(map[string]any)[nativeID].(map[string]any)
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

func TestNativeNameSharedAndEnvironmentNamesDoNotCollide(t *testing.T) {
	seenEnv := map[string]bool{}
	for _, id := range []string{"a-b", "a_b", "A_B", "A-b"} {
		name, env := names(id)
		if name != nativeProviderID || seenEnv[env] || !envName.MatchString(env) {
			t.Fatal("原生供应商名称未共享 custom，或环境变量撞名")
		}
		seenEnv[env] = true
	}
}

func TestTemporaryProvidersKeepSeparateProfilesAndCredentials(t *testing.T) {
	a := New()
	a.NativeHome = t.TempDir()
	original := []byte("model_provider='custom'\n[model_providers.custom]\nname='cc-switch'\nenv_key='OLD_KEY'\n")
	path := filepath.Join(a.NativeHome, "config.toml")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	homes := map[string]string{}
	for _, id := range []string{"first", "second"} {
		p := sampleProvider()
		p.ID, p.Model, p.SecretMode = id, id+"-model", "env_key"
		artifact := renderTest(t, a, p)
		input, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": "fake-" + id})
		if err != nil {
			t.Fatal(err)
		}
		checkLaunchArgs(t, input, artifact)
		homes[id] = input.Env["CODEX_HOME"]
		if len(input.Env) != 3 || input.Env[artifact.EnvKey] != "fake-"+id {
			t.Fatal("供应商凭据未隔离")
		}
	}
	for _, id := range []string{"first", "second"} {
		data, err := os.ReadFile(filepath.Join(homes[id], "config.toml"))
		if err != nil || decodeTOML(t, data)["model"] != id+"-model" {
			t.Fatal("独立 profile 被其他供应商覆盖")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("临时运行修改了主配置")
	}
}

func TestReplaceCustomPreservesScatteredTablesAndComments(t *testing.T) {
	a := New()
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	original := "notes='''\n[model_providers.custom]\n虚构表头\n'''\n[model_providers.'custom'] # 保留表头注释\nname='old' # 保留字段注释\n[model_providers.other]\nname='keep'\n[model_providers.\"custom\".auth] # 保留子表注释\ncommand='old-command'\n[projects.'E:/work']\ntrust_level='trusted'\n[model_providers.custom.query_params]\nversion='old-version'\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(renderTest(t, a, sampleProvider()), home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"# 保留表头注释", "# 保留字段注释", "# 保留子表注释", "[model_providers.other]\nname='keep'", "notes='''\n[model_providers.custom]\n虚构表头\n'''", "[projects.'E:/work']\ntrust_level='trusted'"} {
		if !bytes.Contains(data, []byte(kept)) {
			t.Fatalf("丢失无关内容：%s", kept)
		}
	}
	custom := decodeTOML(t, data)["model_providers"].(map[string]any)[nativeProviderID].(map[string]any)
	if custom["query_params"] != nil || custom["auth"].(map[string]any)["command"] != "relay" {
		t.Fatal("旧供应商分散子表仍然残留")
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
	nativeID, _ := names(p.ID)
	definition := config["model_providers"].(map[string]any)[nativeID].(map[string]any)
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
	original := "# 用户注释\nprofile = 'old-manual' # 保留旧默认的说明\nmodel = \"user-model\" # 保留同行说明\nmodel_provider = 'custom'\nnotes = '''保留多行\n# BEGIN RELAY CODEX not-real\n结束'''\n\n[model_providers.user]\nname = 'user' # 保留供应商\nbase_url = 'https://user.invalid'\n\n# cc-switch 当前活动槽\n[model_providers.custom]\nname = 'cc-switch-old'\nbase_url = 'https://old.example.invalid/v1'\nwire_api = 'responses'\nenv_key = 'OLD_CC_SWITCH_KEY'\n\n[projects.'C:/work']\ntrust_level = 'trusted'\n\n[profiles.historical]\nmodel = 'historical-model'\n"
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
	nativeID, _ := names(p.ID)
	if config["model"] != p.Model || config["model_provider"] != nativeID || config["profile"] != nil {
		t.Fatal("全局指针错误")
	}
	active := config["model_providers"].(map[string]any)[nativeID].(map[string]any)
	if active["base_url"] != p.BaseURL || active["env_key"] != nil || active["auth"] == nil {
		t.Fatal("未定点替换 cc-switch 的 custom 活动槽")
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
	if len(config["model_providers"].(map[string]any)) != 2 {
		t.Fatal("切换时丢失其他已安装 provider")
	}
	a.LaunchMode = "profile"
	input, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatalf("独立 profile 未安装: %v", err)
	}
	checkLaunchArgs(t, input, artifact)
	if err := os.WriteFile(filepath.Join(input.Env["CODEX_HOME"], "config.toml"), []byte("model=???\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BuildLaunchInputs(artifact, nil); err == nil {
		t.Fatal("不应使用已变更的 profile")
	}
}

func TestApplyGlobalIncludesCatalogForDirectCodexLaunch(t *testing.T) {
	a := New()
	p := sampleProvider()
	models := []provider.Model{{
		ProviderID:  p.ID,
		ModelID:     "test-model",
		DisplayName: "测试模型",
		IsDefault:   true,
	}}
	artifact, err := a.Render(p, t.TempDir(), models...)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := a.ApplyGlobal(artifact, home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	config := decodeTOML(t, data)
	if config["model_provider"] != nativeProviderID || config["model"] != p.Model {
		t.Fatalf("全局 Codex 选择器错误: %#v", config)
	}
	catalogPath, ok := config["model_catalog_json"].(string)
	if !ok || catalogPath == "" {
		t.Fatal("switch 后主配置缺少 model_catalog_json，直接启动 Codex 无法加载模型列表")
	}
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatalf("全局模型目录不存在: %v", err)
	}

	withoutModels, err := a.Render(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(withoutModels, home); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	config = decodeTOML(t, data)
	if _, ok := config["model_catalog_json"]; ok {
		t.Fatal("切换到未声明模型目录的供应商后仍残留 model_catalog_json")
	}
}

func TestProfileModeInstallsWithoutChangingDefault(t *testing.T) {
	a := New()
	a.LaunchMode, a.NativeHome = "profile", t.TempDir()
	artifact := renderTest(t, a, sampleProvider())
	if _, err := a.BuildLaunchInputs(artifact, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.NativeHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("临时启动创建或修改了全局配置")
	}
}

func TestProfileContainsSelectorsResetAndCatalog(t *testing.T) {
	a := New()
	a.NativeHome = t.TempDir()
	p := sampleProvider()
	models := []provider.Model{{ProviderID: p.ID, ModelID: "test-model", DisplayName: "测试模型", IsDefault: true}}
	artifact, err := a.Render(p, t.TempDir(), models...)
	if err != nil {
		t.Fatal(err)
	}
	input, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(filepath.Join(input.Env["CODEX_HOME"], "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if decodeTOML(t, profile)["model_providers"].(map[string]any)[nativeProviderID] == nil || !bytes.Contains(profile, []byte("model_catalog_json")) {
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
	// 非标准表结构（如内联字符串）无法定点替换，必须拒绝而不是整份重写。
	for _, text := range []string{"model = ???", "model_providers.custom = 'not-a-table'\n"} {
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
	if err := os.WriteFile(filepath.Join(home, artifact.ProviderID+".config.toml"), []byte("model='manual'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(artifact, home); err == nil {
		t.Fatal("不应覆盖同名用户 profile")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("profile 冲突时不应创建 config.toml")
	}
}

// cc-switch 整份重写 config.toml 时曾把成对标记拆散，只留下孤立的 END。
// 这类残缺标记不得再阻断启动，也不能导致删除它周围的内容。
func TestApplyGlobalToleratesBrokenMarkers(t *testing.T) {
	a := New()
	p := sampleProvider()
	artifact := renderTest(t, a, p)
	nativeID, _ := names(p.ID)
	cases := map[string]string{
		"孤立 END":   "[tui]\nx = 1\n# END RELAY CODEX " + nativeID + "\n\n[projects.'C:/w']\ntrust_level = 'trusted'\n",
		"孤立 BEGIN": "# BEGIN RELAY CODEX " + nativeID + "\nmodel = 'x'\n\n[mcp_servers.node]\ncommand = 'node'\n",
		"标记倒序":     "# END RELAY CODEX " + nativeID + "\n[projects.'C:/w']\ntrust_level = 'trusted'\n# BEGIN RELAY CODEX " + nativeID + "\n",
		"重复配对":     "# BEGIN RELAY CODEX " + nativeID + "\n# END RELAY CODEX " + nativeID + "\n# END RELAY CODEX " + nativeID + "\n[projects.'C:/w']\ntrust_level = 'trusted'\n",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.ApplyGlobal(artifact, home); err != nil {
				t.Fatalf("残缺标记不应阻断写入: %v", err)
			}
			merged, _ := os.ReadFile(path)
			// 用户内容必须完整保留，只有标记行被清理。
			if name != "重复配对" && !bytes.Contains(merged, []byte("trust_level = 'trusted'")) && !bytes.Contains(merged, []byte("[mcp_servers.node]")) {
				t.Fatalf("误删了用户内容: %s", merged)
			}
			if bytes.Count(merged, []byte("# BEGIN RELAY CODEX ")) != 1 || bytes.Count(merged, []byte("# END RELAY CODEX ")) != 1 {
				t.Fatalf("未清理残缺标记: %s", merged)
			}
			config := decodeTOML(t, merged)
			if config["model_provider"] != nativeID {
				t.Fatal("未写入全局指针")
			}
			if _, ok := config["model_providers"].(map[string]any)[nativeID]; !ok {
				t.Fatal("未写入活动槽")
			}
		})
	}
}

// 标记行只清理自身；表内容由 removeProviderTable 按结构删除，注释错位不会误删配置。
func TestRemoveManagedMarkersOnlyRemovesMarkers(t *testing.T) {
	nativeID, _ := names("test-provider")
	content := []byte("# 用户注释\nkeep = 1\n\n# BEGIN RELAY CODEX " + nativeID + "\nmodel_providers.custom = { base_url = 'https://old.invalid' }\n# END RELAY CODEX " + nativeID + "\n\n[projects.'C:/w']\ntrust_level = 'trusted'\n")
	result, err := removeManagedMarkers(content, nativeID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, []byte("RELAY CODEX")) {
		t.Fatalf("未清理标记行: %s", result)
	}
	// 标记不再决定删除范围，表内容的取舍交给结构化移除。
	for _, keep := range []string{"# 用户注释", "keep = 1", "old.invalid", "[projects.'C:/w']"} {
		if !bytes.Contains(result, []byte(keep)) {
			t.Fatalf("误删内容 %s: %s", keep, result)
		}
	}
	// 其他 id 的标记与字符串中的相同文字都不受影响。
	other := []byte("# END RELAY CODEX another-provider\nnotes = '''\n# BEGIN RELAY CODEX " + nativeID + "\n'''\n")
	result, err = removeManagedMarkers(other, nativeID)
	if err != nil || !bytes.Equal(result, other) {
		t.Fatalf("不应改动其他 id 的标记或多行字符串: %v %s", err, result)
	}
	// 旧版按供应商 ID 命名的标记也必须一并清理，否则切换后仍会残留。
	legacy := []byte("# BEGIN RELAY CODEX axonhub-lite\nx = 1\n# END RELAY CODEX axonhub-lite\n# END RELAY CODEX axonhub-lite\n")
	result, err = removeManagedMarkers(legacy, nativeID, "axonhub-lite")
	if err != nil || bytes.Contains(result, []byte("RELAY CODEX")) || !bytes.Contains(result, []byte("x = 1")) {
		t.Fatalf("未清理旧版命名标记: %v %s", err, result)
	}
}

// cc-switch 重排注释后，标记可能包住别的供应商表；结构化移除不能因此带走它。
func TestApplyGlobalDoesNotDeleteBehindMisplacedMarkers(t *testing.T) {
	a := New()
	artifact := renderTest(t, a, sampleProvider())
	nativeID, _ := names(sampleProvider().ID)
	original := "# BEGIN RELAY CODEX " + nativeID + "\n\n[model_providers.other]\nname = 'other'\nbase_url = 'https://other.invalid'\n# END RELAY CODEX " + nativeID + "\n"
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyGlobal(artifact, home); err != nil {
		t.Fatal(err)
	}
	merged, _ := os.ReadFile(path)
	if !bytes.Contains(merged, []byte("[model_providers.other]")) {
		t.Fatalf("误删了被标记包住的其他供应商: %s", merged)
	}
	config := decodeTOML(t, merged)
	if _, ok := config["model_providers"].(map[string]any)["other"]; !ok {
		t.Fatal("其他供应商表丢失")
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
