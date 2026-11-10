package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const existingNotifyConfig = `# 用户注释
notify = ["C:\\old\\prog.exe", "turn-ended"]
model = "gpt-demo"

[custom]
value = 'keep'
`

func readTop(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := toml.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	return top
}

func notifyStrings(t *testing.T, top map[string]any) []string {
	t.Helper()
	raw, ok := top["notify"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

func TestNotifyInstallChainsAndRestores(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(existingNotifyConfig), 0600); err != nil {
		t.Fatal(err)
	}
	command := []string{`C:\new\relay.exe`, "notify", "hook", "codex"}

	previous, replacedOther, err := NotifyInstall(home, command)
	if err != nil {
		t.Fatal(err)
	}
	if !replacedOther || len(previous) != 2 || previous[0] != `C:\old\prog.exe` || previous[1] != "turn-ended" {
		t.Fatalf("链式接管应返回旧 notify: %#v", previous)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# 用户注释") || !strings.Contains(string(raw), "value = 'keep'") {
		t.Fatalf("用户注释与无关配置被破坏:\n%s", raw)
	}
	top := readTop(t, path)
	current := notifyStrings(t, top)
	if len(current) != 4 || current[0] != `C:\new\relay.exe` || strings.Join(current[1:], " ") != "notify hook codex" {
		t.Fatalf("notify 未替换为 relay 命令: %#v", current)
	}
	if top["model"] != "gpt-demo" {
		t.Fatal("无关顶层键丢失")
	}

	if _, ok := top["custom"].(map[string]any); !ok {
		t.Fatal("子表丢失")
	}

	current, ours, err := NotifyInspect(home)
	if err != nil || !ours || len(current) != 4 {
		t.Fatalf("NotifyInspect 不符: %#v %v %v", current, ours, err)
	}

	// relay 自己的条目重复安装：不覆盖状态里的 previous，也不报错。
	_, replacedAgain, err := NotifyInstall(home, command)
	if err != nil || replacedAgain {
		t.Fatalf("重复安装应视为已接管: %v %v", replacedAgain, err)
	}

	restored, err := NotifyRestore(home, previous)
	if err != nil || !restored {
		t.Fatalf("应还原旧 notify: %v %v", restored, err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "# 用户注释") {
		t.Fatalf("还原后用户注释丢失:\n%s", raw)
	}
	if current = notifyStrings(t, readTop(t, path)); len(current) != 2 || current[0] != `C:\old\prog.exe` {
		t.Fatalf("还原值不符: %#v", current)
	}
	// 已还原后再还原应是无操作。
	if restored, err := NotifyRestore(home, previous); err != nil || restored {
		t.Fatalf("无 relay 条目时还原应为无操作: %v %v", restored, err)
	}
}

func TestNotifyInstallWithoutExisting(t *testing.T) {
	home := t.TempDir()
	previous, replacedOther, err := NotifyInstall(home, []string{"relay", "notify", "hook", "codex"})
	if err != nil || replacedOther || previous != nil {
		t.Fatalf("无旧值时应直接安装: %#v %v %v", previous, replacedOther, err)
	}
	if _, ours, _ := NotifyInspect(home); !ours {
		t.Fatal("安装后应为 relay 条目")
	}
	// previous 为 nil 表示删除 notify 键。
	if restored, err := NotifyRestore(home, nil); err != nil || !restored {
		t.Fatalf("应删除 notify 键: %v %v", restored, err)
	}
	if current, ours, _ := NotifyInspect(home); ours || current != nil {
		t.Fatalf("删除后不应再有 notify: %#v %v", current, ours)
	}
}

func TestNotifyRestoreSkipsUserModified(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if _, _, err := NotifyInstall(home, []string{"relay", "notify", "hook", "codex"}); err != nil {
		t.Fatal(err)
	}
	// 用户在 relay 接管后手动改了 notify：还原必须让位。
	manual := "notify = [\"manual.exe\"]\n"
	if err := os.WriteFile(path, []byte(manual), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := NotifyRestore(home, nil)
	if err != nil || restored {
		t.Fatalf("用户改过的 notify 不应被还原覆盖: %v %v", restored, err)
	}
	if current := notifyStrings(t, readTop(t, path)); len(current) != 1 || current[0] != "manual.exe" {
		t.Fatalf("手动值被改动: %#v", current)
	}
}

func TestNotifyRefusesInvalidValues(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("notify = \"not-array\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NotifyInstall(home, []string{"relay", "notify", "hook", "codex"}); err == nil {
		t.Fatal("notify 非字符串数组应拒绝")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "notify = \"not-array\"\n" {
		t.Fatal("拒绝时不应修改文件")
	}
}
