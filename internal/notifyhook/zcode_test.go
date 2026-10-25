package notifyhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const userZcodeConfig = `{
  "mcp": {"servers": {"demo": {}}},
  "hooks": {
    "enabled": false,
    "events": {
      "UserPromptSubmit": [{"hooks":[{"type":"command","command":"user-hook"}]}]
    }
  }
}`

func readZcode(t *testing.T, path string) (enabled *bool, events map[string][]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Hooks struct {
			Enabled *bool            `json:"enabled"`
			Events  map[string][]any `json:"events"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object.Hooks.Enabled, object.Hooks.Events
}

func TestZcodeInstallEnablesAndRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(userZcodeConfig), 0600); err != nil {
		t.Fatal(err)
	}
	target := ZcodeTarget{ConfigPath: path}
	binary := filepath.Join(t.TempDir(), "relay.exe")

	previous, err := target.Install(binary)
	if err != nil {
		t.Fatal(err)
	}
	if previous == nil || *previous != false {
		t.Fatalf("应记录安装前 enabled=false: %#v", previous)
	}
	enabled, events := readZcode(t, path)
	if enabled == nil || !*enabled {
		t.Fatal("安装后 hooks.enabled 应为 true")
	}
	for _, name := range []string{"Stop", "PermissionRequest", "PreToolUse"} {
		if len(events[name]) != 1 {
			t.Fatalf("%s 应有 1 条 relay 条目: %#v", name, events[name])
		}
	}
	if len(events["UserPromptSubmit"]) != 1 {
		t.Fatal("用户已有事件被改动")
	}
	entry := events["PreToolUse"][0].(map[string]any)
	if entry["matcher"] != "AskUserQuestion|ExitPlanMode" {
		t.Fatalf("PreToolUse matcher 不符: %#v", entry)
	}
	hook := entry["hooks"].([]any)[0].(map[string]any)
	if hook["type"] != "process" || hook["command"] != binary || hook["timeoutMs"] != float64(10000) {
		t.Fatalf("process 条目不符: %#v", hook)
	}
	args := hook["args"].([]any)
	if len(args) != 3 || args[0] != "notify" || args[1] != "hook" || args[2] != "zcode" {
		t.Fatalf("args 不符: %#v", args)
	}

	// 幂等：重复安装不产生重复条目。
	if _, err := target.Install(filepath.Join(t.TempDir(), "relay2.exe")); err != nil {
		t.Fatal(err)
	}
	_, events = readZcode(t, path)
	if len(events["Stop"]) != 1 || len(events["UserPromptSubmit"]) != 1 {
		t.Fatalf("重复安装不幂等: %#v", events)
	}

	found, err := target.Status()
	if err != nil || len(found) != 3 {
		t.Fatalf("Status 不符: %#v %v", found, err)
	}

	removed, err := target.Uninstall(previous)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("应移除 3 个事件: %#v", removed)
	}
	enabled, events = readZcode(t, path)
	if enabled == nil || *enabled {
		t.Fatal("卸载后 enabled 应回到安装前的 false")
	}
	if len(events["UserPromptSubmit"]) != 1 {
		t.Fatal("用户已有事件应保留")
	}
	if len(events) != 1 {
		t.Fatalf("relay 事件应被清除: %#v", events)
	}
}

func TestZcodeInstallCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	target := ZcodeTarget{ConfigPath: path}
	previous, err := target.Install("relay")
	if err != nil {
		t.Fatal(err)
	}
	if previous != nil {
		t.Fatalf("全新配置的 previous 应为 nil: %#v", previous)
	}
	enabled, events := readZcode(t, path)
	if enabled == nil || !*enabled || len(events["Stop"]) != 1 {
		t.Fatal("应创建配置并写入条目")
	}
}
