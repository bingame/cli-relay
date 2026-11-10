package notifyhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/adapter/claudecode"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func eventEntries(t *testing.T, object map[string]any, event string) []any {
	t.Helper()
	hooks, _ := object["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	entries, _ := hooks[event].([]any)
	return entries
}

// 模拟外部编排工具（如 .orca）写入的用户级 hooks，安装后必须原样保留。
const orcaSettings = `{
  "env": {"OTHER": "1"},
  "hooks": {
    "SessionStart": [{"hooks":[{"type":"command","command":"orca-session","timeout":10}]}],
    "Stop": [{"hooks":[{"type":"command","command":"orca-stop","timeout":10}]}],
    "PreToolUse": [{"matcher":"*","hooks":[{"type":"command","command":"orca-pre","timeout":10}]}]
  }
}`

func TestClaudeInstallCoexistsIdempotentAndUninstall(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, "settings.json")
	if err := os.WriteFile(settings, []byte(orcaSettings), 0600); err != nil {
		t.Fatal(err)
	}
	target := ClaudeTarget{NativeHome: home}
	binary := filepath.Join(t.TempDir(), "relay.exe")

	command, err := target.Install(binary)
	if err != nil {
		t.Fatal(err)
	}
	object := readJSON(t, settings)
	if got := eventEntries(t, object, "Stop"); len(got) != 2 {
		t.Fatalf("Stop 应追加 1 条而共 2 条: %#v", got)
	}
	if got := eventEntries(t, object, "Notification"); len(got) != 1 {
		t.Fatalf("Notification 应新建 1 条: %#v", got)
	}
	pre := eventEntries(t, object, "PreToolUse")
	if len(pre) != 2 {
		t.Fatalf("PreToolUse 应追加 1 条而共 2 条: %#v", pre)
	}
	ours := pre[1].(map[string]any)
	if ours["matcher"] != "AskUserQuestion|ExitPlanMode" {
		t.Fatalf("PreToolUse matcher 不符: %#v", ours)
	}
	if got := eventEntries(t, object, "SessionStart"); len(got) != 1 {
		t.Fatal("SessionStart 不应被改动")
	}
	if _, ok := object["env"]; !ok {
		t.Fatal("无关顶层键丢失")
	}
	if command == "" || !containsCommand(t, ours, command) {
		t.Fatalf("安装命令未写入: %q", command)
	}

	// 重复安装（二进制移动后）必须幂等，且更新为新的命令串。
	newBinary := filepath.Join(t.TempDir(), "relay-new.exe")
	newCommand, err := target.Install(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	object = readJSON(t, settings)
	if got := eventEntries(t, object, "Stop"); len(got) != 2 {
		t.Fatalf("重复安装后 Stop 条目数应为 2: %#v", got)
	}
	if newCommand == command {
		t.Fatal("命令串应随二进制路径更新")
	}

	events, err := target.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("Status 应报告 3 个事件: %#v", events)
	}

	removed, err := target.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("应删除 3 个事件条目: %#v", removed)
	}
	object = readJSON(t, settings)
	if got := eventEntries(t, object, "Stop"); len(got) != 1 || !containsCommand(t, got[0].(map[string]any), "orca-stop") {
		t.Fatalf("orca 的 Stop 条目被误删: %#v", got)
	}
	if got := eventEntries(t, object, "PreToolUse"); len(got) != 1 {
		t.Fatalf("orca 的 PreToolUse 条目被误删: %#v", got)
	}
	if got := eventEntries(t, object, "SessionStart"); len(got) != 1 {
		t.Fatal("SessionStart 不应被改动")
	}
	if _, ok := readJSON(t, settings)["hooks"].(map[string]any)["Notification"]; ok {
		t.Fatal("新建的 Notification 事件应整体移除")
	}
	// 再卸载一次应为无操作。
	if removed, err := target.Uninstall(); err != nil || len(removed) != 0 {
		t.Fatalf("空卸载应无操作: %#v %v", removed, err)
	}
}

func containsCommand(t *testing.T, entry map[string]any, command string) bool {
	t.Helper()
	inner, _ := entry["hooks"].([]any)
	for _, hook := range inner {
		h, _ := hook.(map[string]any)
		if h["command"] == command {
			return true
		}
	}
	return false
}

func TestClaudeInstallCreatesSettings(t *testing.T) {
	home := t.TempDir()
	target := ClaudeTarget{NativeHome: home}
	if _, err := target.Install(filepath.Join(t.TempDir(), "relay.exe")); err != nil {
		t.Fatal(err)
	}
	object := readJSON(t, filepath.Join(home, "settings.json"))
	if len(eventEntries(t, object, "Stop")) != 1 {
		t.Fatal("settings.json 不存在时应创建并写入条目")
	}
}

func TestClaudeRefusesInvalidSettings(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	target := ClaudeTarget{NativeHome: home}
	if _, err := target.Install("relay"); err == nil {
		t.Fatal("非对象 settings.json 应拒绝")
	}
}

// 通知 hooks 与 relay switch 的全局字段托管互不干扰：
// switch 之后 hooks 条目必须仍在，卸载 hooks 也不能动 switch 托管的字段。
func TestClaudeHooksSurviveSwitch(t *testing.T) {
	home := t.TempDir()
	target := ClaudeTarget{NativeHome: home}
	if _, err := target.Install("relay.exe"); err != nil {
		t.Fatal(err)
	}
	artifact := adapter.Artifact{Target: "claude", ProviderID: "p1", Config: map[string]any{"model": "m1"}}
	if err := claudecode.New().ApplyGlobal(artifact, home); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, "settings.json")
	object := readJSON(t, settings)
	if object["model"] != "m1" {
		t.Fatalf("switch 未生效: %#v", object)
	}
	if len(eventEntries(t, object, "Stop")) != 1 || len(eventEntries(t, object, "Notification")) != 1 {
		t.Fatal("relay switch 之后通知 hooks 丢失")
	}
	if _, err := target.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if object = readJSON(t, settings); object["model"] != "m1" {
		t.Fatalf("卸载通知 hooks 影响了 switch 托管字段: %#v", object)
	}
}
