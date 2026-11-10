package notifyhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bingame/cli-relay/internal/safeio"
)

// ZcodeTarget 写 ZCode 用户级 ~/.zcode/cli/config.json 的 hooks.events。
// 配置级 hooks 默认禁用，安装会同时置 enabled=true，卸载时还原原值。
type ZcodeTarget struct{ ConfigPath string }

var zcodeNotifyEvents = []struct{ Name, Matcher string }{
	{"Stop", ""},
	{"PermissionRequest", ""},
	{"PreToolUse", "AskUserQuestion|ExitPlanMode"},
}

// ZcodeConfigPath 返回用户级 ZCode 配置文件路径。
func ZcodeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".zcode", "cli", "config.json"), nil
}

func (t ZcodeTarget) read() (map[string]any, error) {
	current := map[string]any{}
	raw, err := safeio.ReadRegular(t.ConfigPath, 8<<20)
	if errors.Is(err, os.ErrNotExist) {
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &current); err != nil || current == nil {
		return nil, fmt.Errorf("已有 ZCode config.json 不是合法 JSON 对象，未修改")
	}
	return current, nil
}

func (t ZcodeTarget) write(current map[string]any) error {
	content, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码 ZCode 配置")
	}
	return safeio.WriteFile(t.ConfigPath, append(content, '\n'), 0600)
}

// Install 幂等安装通知条目并启用 hooks，返回安装前的 enabled 值（nil 表示原本未设置）。
func (t ZcodeTarget) Install(binary string) (*bool, error) {
	current, err := t.read()
	if err != nil {
		return nil, err
	}
	hooks, _ := current["hooks"].(map[string]any)
	if hooks == nil {
		if _, taken := current["hooks"]; taken {
			return nil, fmt.Errorf("ZCode config.json 的 hooks 不是对象，未修改")
		}
		hooks = map[string]any{}
	}
	var previous *bool
	if enabled, ok := hooks["enabled"].(bool); ok {
		value := enabled
		previous = &value
	}
	events, _ := hooks["events"].(map[string]any)
	if events == nil {
		if _, taken := hooks["events"]; taken {
			return nil, fmt.Errorf("ZCode config.json 的 hooks.events 不是对象，未修改")
		}
		events = map[string]any{}
	}
	for _, event := range zcodeNotifyEvents {
		events[event.Name] = appendZcodeFiltered(events[event.Name], event.Matcher, binary)
	}
	hooks["events"] = events
	hooks["enabled"] = true
	current["hooks"] = hooks
	if err := t.write(current); err != nil {
		return nil, err
	}
	return previous, nil
}

// Uninstall 删除 relay 的条目；全部清空后按 previous 还原 enabled。
func (t ZcodeTarget) Uninstall(previous *bool) ([]string, error) {
	current, err := t.read()
	if err != nil {
		return nil, err
	}
	hooks, ok := current["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	events, ok := hooks["events"].(map[string]any)
	if !ok {
		return nil, nil
	}
	removed := []string{}
	for _, name := range sortedKeys(events) {
		entries, ok := events[name].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, entry := range entries {
			if isZcodeOurs(entry) {
				removed = append(removed, name)
			} else {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(events, name)
		} else {
			events[name] = kept
		}
	}
	if len(removed) == 0 {
		return removed, nil
	}
	if len(events) == 0 {
		delete(hooks, "events")
	}
	// enabled 始终还原到安装前的值：用户可能保留着事件但整体处于禁用状态。
	if previous != nil {
		hooks["enabled"] = *previous
	} else if len(events) == 0 {
		delete(hooks, "enabled")
	}
	if len(hooks) == 0 {
		delete(current, "hooks")
	}
	if err := t.write(current); err != nil {
		return nil, err
	}
	return removed, nil
}

// Status 列出当前包含 relay 条目的事件名。
func (t ZcodeTarget) Status() ([]string, error) {
	current, err := t.read()
	if err != nil {
		return nil, err
	}
	hooks, ok := current["hooks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	events, ok := hooks["events"].(map[string]any)
	if !ok {
		return nil, nil
	}
	found := []string{}
	for _, name := range sortedKeys(events) {
		entries, ok := events[name].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			if isZcodeOurs(entry) {
				found = append(found, name)
				break
			}
		}
	}
	return found, nil
}

func appendZcodeFiltered(existing any, matcher, binary string) []any {
	entries, _ := existing.([]any)
	kept := []any{}
	for _, entry := range entries {
		if !isZcodeOurs(entry) {
			kept = append(kept, entry)
		}
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":      "process",
			"command":   binary,
			"args":      []string{"notify", "hook", "zcode"},
			"timeoutMs": 10000,
		}},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	return append(kept, entry)
}

func isZcodeOurs(entry any) bool {
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
		if !ok || h["type"] != "process" {
			continue
		}
		args, ok := h["args"].([]any)
		if ok && len(args) >= 3 {
			a0, _ := args[0].(string)
			a1, _ := args[1].(string)
			a2, _ := args[2].(string)
			if a0 == "notify" && a1 == "hook" && a2 == "zcode" {
				return true
			}
		}
	}
	return false
}
