package notifyhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
)

// ClaudeTarget 写 Claude Code 用户级 settings.json 的 hooks。
// 与 ApplyGlobal 共用同一把锁与读改写流程，但只按条目增删，不碰
// .relay-managed.json 的叶子字段托管，用户已有 hooks（如外部编排工具
// 写入的条目）原样保留。
type ClaudeTarget struct{ NativeHome string }

var claudeNotifyEvents = []struct{ Name, Matcher string }{
	{"Stop", ""},
	{"Notification", ""},
	{"PreToolUse", "AskUserQuestion|ExitPlanMode"},
}

// ClaudeCommand 生成写入 hook 的命令串；用引号包裹的正斜杠绝对路径，
// cmd 与 bash 下都可执行。
func ClaudeCommand(binary string) string {
	return "\"" + filepath.ToSlash(binary) + "\" notify hook claude"
}

func (t ClaudeTarget) settingsPath() string { return filepath.Join(t.NativeHome, "settings.json") }

func (t ClaudeTarget) lock() (*flock.Flock, error) {
	if err := safeio.EnsureDir(t.NativeHome); err != nil {
		return nil, err
	}
	path := filepath.Join(t.NativeHome, ".relay-settings.lock")
	if err := safeio.CheckPath(path); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("无法锁定 Claude Code 配置: %w", err)
	}
	return lock, nil
}

func (t ClaudeTarget) read() (map[string]any, []byte, bool, error) {
	current := map[string]any{}
	before, err := safeio.ReadRegular(t.settingsPath(), 16<<20)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, err
	}
	if existed {
		if err := json.Unmarshal(before, &current); err != nil || current == nil {
			return nil, nil, false, fmt.Errorf("已有 Claude Code settings.json 不是合法 JSON 对象，未修改")
		}
	}
	return current, before, existed, nil
}

func (t ClaudeTarget) write(current map[string]any) error {
	content, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码 Claude Code 配置")
	}
	return safeio.WriteFile(t.settingsPath(), append(content, '\n'), 0600)
}

// Install 幂等安装三个通知 hook 条目，返回实际命令串。
func (t ClaudeTarget) Install(binary string) (string, error) {
	lock, err := t.lock()
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	current, _, _, err := t.read()
	if err != nil {
		return "", err
	}
	command := ClaudeCommand(binary)
	hooks, _ := current["hooks"].(map[string]any)
	if hooks == nil {
		if _, taken := current["hooks"]; taken {
			return "", fmt.Errorf("Claude Code settings.json 的 hooks 不是对象，未修改")
		}
		hooks = map[string]any{}
	}
	for _, event := range claudeNotifyEvents {
		hooks[event.Name] = appendFiltered(hooks[event.Name], event.Matcher, command)
		current["hooks"] = hooks
	}
	if err := t.write(current); err != nil {
		return "", err
	}
	return command, nil
}

// Uninstall 从所有事件数组中删除 relay 的条目，返回删除的事件名。
func (t ClaudeTarget) Uninstall() ([]string, error) {
	lock, err := t.lock()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	current, _, _, err := t.read()
	if err != nil {
		return nil, err
	}
	hooks, ok := current["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	removed := []string{}
	for _, name := range sortedKeys(hooks) {
		entries, ok := hooks[name].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, entry := range entries {
			if isClaudeOurs(entry) {
				removed = append(removed, name)
			} else {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(hooks, name)
		} else {
			hooks[name] = kept
		}
	}
	if len(hooks) == 0 {
		delete(current, "hooks")
	}
	if len(removed) == 0 {
		return removed, nil
	}
	if err := t.write(current); err != nil {
		return nil, err
	}
	return removed, nil
}

// Status 列出当前包含 relay 条目的事件名。
func (t ClaudeTarget) Status() ([]string, error) {
	current, _, _, err := t.read()
	if err != nil {
		return nil, err
	}
	hooks, ok := current["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	found := []string{}
	for _, name := range sortedKeys(hooks) {
		entries, ok := hooks[name].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			if isClaudeOurs(entry) {
				found = append(found, name)
				break
			}
		}
	}
	return found, nil
}

// appendFiltered 移除同名事件里 relay 的旧条目后追加当前条目，保证幂等。
func appendFiltered(existing any, matcher, command string) []any {
	entries, _ := existing.([]any)
	kept := []any{}
	for _, entry := range entries {
		if !isClaudeOurs(entry) {
			kept = append(kept, entry)
		}
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	return append(kept, entry)
}

func isClaudeOurs(entry any) bool {
	object, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := object["hooks"].([]any)
	if !ok {
		return false
	}
	for _, hook := range inner {
		h, ok := hook.(map[string]any)
		if !ok || h["type"] != "command" {
			continue
		}
		if command, _ := h["command"].(string); strings.Contains(command, "notify hook claude") {
			return true
		}
	}
	return false
}

func sortedKeys[V any](object map[string]V) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
