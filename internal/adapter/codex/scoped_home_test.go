package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
)

func TestScopedHomeInheritanceAndPersistence(t *testing.T) {
	a := New()
	a.NativeHome = t.TempDir()
	t.Setenv("CODEX_SQLITE_HOME", "")
	base := []byte("cli_auth_credentials_store='keyring'\nmcp_oauth_credentials_store='auto'\nmodel='base'\nmodel_reasoning_effort='low'\nmodel_instructions_file='instructions.md'\nnotify=['fake-notifier']\n[model_providers.custom]\nname='old'\nexperimental_bearer_token='fake-old-token'\n[projects.'test-project']\ntrust_level='trusted'\n[mcp_servers.example]\ncommand='fake-mcp'\n[mcp_servers.example.env]\nAPI_KEY='fake-mcp-secret'\nNORMAL='setting'\n[mcp_servers.remote]\nurl='https://example.invalid/mcp'\n[mcp_servers.remote.http_headers]\nAuthorization='Bearer fake-header'\n")
	basePath := filepath.Join(a.NativeHome, "config.toml")
	if err := os.WriteFile(basePath, base, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.NativeHome, "skills", "probe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.NativeHome, "skills", "probe", "SKILL.md"), []byte("shared skill"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact := renderTest(t, a, sampleProvider())
	input, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(input.Env["CODEX_HOME"], "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fake-old-token", "fake-mcp-secret", "fake-header"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("凭据进入隔离配置")
		}
	}
	config := decodeTOML(t, data)
	if config["model_reasoning_effort"] != "low" || config["model_instructions_file"] != filepath.Join(a.NativeHome, "instructions.md") || config["projects"] == nil || config["notify"] == nil {
		t.Fatal("未继承原生设置或路径")
	}
	if input.Env["API_KEY"] != "fake-mcp-secret" {
		t.Fatal("MCP 凭据未经环境传递")
	}
	if data, err := os.ReadFile(filepath.Join(input.Env["CODEX_HOME"], "skills", "probe", "SKILL.md")); err != nil || string(data) != "shared skill" {
		t.Fatal("Skills 未共享", err)
	}
	if err := os.WriteFile(filepath.Join(input.Env["CODEX_HOME"], "sessions", "probe.jsonl"), []byte("shared rollout"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(a.NativeHome, "sessions", "probe.jsonl")); err != nil || string(data) != "shared rollout" {
		t.Fatal("新会话未保存到原生目录")
	}
	selected, err := setTopLevelValues(data, map[string]any{"model": "selected", "model_reasoning_effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, selected, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Env["CODEX_HOME"] != input.Env["CODEX_HOME"] || second.Env["API_KEY"] != "fake-mcp-secret" {
		t.Fatal("重复生成丢失目录或 MCP 凭据")
	}
	data, _ = os.ReadFile(path)
	if got := decodeTOML(t, data); got["model"] != "selected" || got["model_reasoning_effort"] != "high" {
		t.Fatal("重新生成覆盖 TUI 偏好")
	}
	p := sampleProvider()
	p.Model = "new-provider-default"
	updated, err := a.Render(p, filepath.Dir(filepath.Dir(filepath.Dir(artifact.Path))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BuildLaunchInputs(updated, nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if decodeTOML(t, data)["model"] != p.Model {
		t.Fatal("供应商显式默认变更未生效")
	}
	unchanged, _ := os.ReadFile(basePath)
	if !bytes.Equal(unchanged, base) {
		t.Fatal("修改了全局配置")
	}
	// 嵌套启动解析回原始目录，避免无限叠加 .relay 子目录。
	a.NativeHome = input.Env["CODEX_HOME"]
	nested, err := a.BuildLaunchInputs(updated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nested.Env["CODEX_HOME"] != input.Env["CODEX_HOME"] {
		t.Fatal("嵌套启动改变隔离目录")
	}
}

func TestScopedConcurrentProvidersAndRelayRoots(t *testing.T) {
	native := t.TempDir()
	root := t.TempDir()
	var artifacts []adapter.Artifact
	for _, id := range []string{"first", "second"} {
		p := sampleProvider()
		p.ID = id
		artifact, err := New().Render(p, root)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}
	var wg sync.WaitGroup
	for _, artifact := range artifacts {
		wg.Go(func() {
			a := New()
			a.NativeHome = native
			if _, err := a.BuildLaunchInputs(artifact, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	a := New()
	a.NativeHome = native
	otherRoot := renderTest(t, a, sampleProvider())
	if scopedHomePath(artifacts[0], native) == scopedHomePath(artifacts[1], native) || scopedHomePath(artifacts[0], native) == scopedHomePath(otherRoot, native) {
		t.Fatal("隔离目录发生冲突")
	}
}
