// Package notifyhook 实现 relay notify 的 hook 事件解析、三家 CLI 的通知条目
// 安装/卸载，以及远程推送发送。所有配置写入都只动 relay 自己的条目，与用户已有
// hooks 追加式共存。
package notifyhook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	KindTaskCompleted = "task_completed"
	KindNeedsHuman    = "needs_human"
)

// Event 是各 CLI hook 载荷归一化后的结果；Skip 表示解析成功但无需通知。
type Event struct {
	Kind    string
	CLI     string
	Session string
	Project string
	Message string
	Skip    bool
}

var questionTools = map[string]bool{"AskUserQuestion": true, "ExitPlanMode": true}

// ParseStdinEvent 解析 Claude Code 与 ZCode 共用的 stdin JSON 载荷。
func ParseStdinEvent(cli string, data []byte) (Event, error) {
	var raw struct {
		HookEventName  string         `json:"hook_event_name"`
		SessionID      string         `json:"session_id"`
		CWD            string         `json:"cwd"`
		Message        string         `json:"message"`
		StopHookActive bool           `json:"stop_hook_active"`
		ToolName       string         `json:"tool_name"`
		ToolInput      map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Event{}, fmt.Errorf("hook 载荷不是合法 JSON")
	}
	ev := Event{CLI: cli, Session: raw.SessionID, Project: projectName(raw.CWD)}
	switch {
	case raw.HookEventName == "Stop":
		// Stop hook 触发续跑时 stop_hook_active 为 true，此时再通知会造成重复。
		if raw.StopHookActive {
			ev.Skip = true
			return ev, nil
		}
		ev.Kind = KindTaskCompleted
	case raw.HookEventName == "Notification" || raw.HookEventName == "PermissionRequest":
		ev.Kind = KindNeedsHuman
		ev.Message = truncate(firstNonEmpty(raw.Message, raw.ToolName), 160)
	case raw.HookEventName == "PreToolUse" && questionTools[raw.ToolName]:
		ev.Kind = KindNeedsHuman
		ev.Message = truncate(questionPreview(raw.ToolInput, raw.ToolName), 160)
	default:
		ev.Skip = true
	}
	return ev, nil
}

// ParseCodexEvent 解析 Codex notify 追加在 argv 末尾的 JSON 载荷。
func ParseCodexEvent(payload string) (Event, error) {
	var raw struct {
		Type                 string `json:"type"`
		ThreadID             string `json:"thread_id"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return Event{}, fmt.Errorf("Codex notify 载荷不是合法 JSON")
	}
	ev := Event{CLI: "codex", Session: raw.ThreadID, Project: projectName("")}
	switch raw.Type {
	case "agent-turn-complete":
		ev.Kind = KindTaskCompleted
	case "approval-requested":
		ev.Kind = KindNeedsHuman
	default:
		ev.Skip = true
	}
	ev.Message = truncate(raw.LastAssistantMessage, 160)
	return ev, nil
}

func projectName(cwd string) string {
	if cwd == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "-"
		}
		cwd = dir
	}
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "-"
	}
	return base
}

func questionPreview(input map[string]any, toolName string) string {
	if input != nil {
		if questions, ok := input["questions"].([]any); ok && len(questions) > 0 {
			if q, ok := questions[0].(map[string]any); ok {
				if text := firstNonEmpty(stringField(q, "question"), stringField(q, "header")); text != "" {
					return text
				}
			}
		}
		for _, key := range []string{"prompt", "plan"} {
			if text := stringField(input, key); text != "" {
				return text
			}
		}
	}
	return toolName
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
