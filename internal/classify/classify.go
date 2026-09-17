package classify

import (
	"encoding/json"
	"strings"
)

const (
	Success    = 0
	Retryable  = 10
	NeedsHuman = 11
	ColdStart  = 12
)

func Error(text string) int {
	s := strings.ToLower(text)
	for _, v := range []string{"unresumablehistory", "resumerejected", "invalid encrypted content", "session history is corrupted", "no rollout found for thread", "session not found"} {
		if strings.Contains(s, v) {
			return ColdStart
		}
	}
	for _, v := range []string{"stream disconnected", "connection closed", "connection reset", "connection refused", "i/o timeout", "request timed out", "temporary failure", "rate limit", "too many requests", "http 429", "status 429", "status 502", "status 503", "status 504", "overloaded"} {
		if strings.Contains(s, v) {
			return Retryable
		}
	}
	return 0
}

type Event struct {
	SessionID string
	WorkDir   string
	Code      int
	Failed    bool
	Completed bool
	Text      string
}

func Parse(line []byte) Event {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return Event{}
	}
	e := Event{}
	e.WorkDir = str(m["cwd"])
	method, _ := m["method"].(string)
	if params, ok := m["params"].(map[string]any); ok {
		if method == "thread/started" {
			if thread, ok := params["thread"].(map[string]any); ok {
				e.SessionID = str(thread["id"])
				e.WorkDir = str(thread["cwd"])
			}
		}
		if strings.HasPrefix(method, "turn/") {
			if id := str(params["threadId"]); id != "" {
				e.SessionID = id
			}
		}
	}
	for _, key := range []string{"session_id", "thread_id", "threadId"} {
		if s, ok := m[key].(string); ok {
			e.SessionID = s
			break
		}
	}
	typ, _ := m["type"].(string)
	if typ == "turn.completed" || (typ == "result" && m["is_error"] != true && !strings.HasPrefix(str(m["subtype"]), "error")) {
		e.Completed = true
	}
	if typ == "error" || typ == "turn.failed" || typ == "response.failed" || m["is_error"] == true || (typ == "result" && strings.HasPrefix(str(m["subtype"]), "error")) {
		e.Failed = true
		e.Code = Error(string(line))
	}
	if humanTool(m) {
		e.Code = NeedsHuman
	}
	if typ == "result" {
		e.Text = str(m["result"])
	}
	if typ == "item.completed" {
		if item, ok := m["item"].(map[string]any); ok && item["type"] == "agent_message" {
			e.Text = str(item["text"])
		}
	}
	return e
}
func humanTool(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		typ := str(v["type"])
		name := str(v["name"])
		if name == "" {
			name = str(v["tool_name"])
		}
		if (typ == "tool_use" || typ == "tool_call" || typ == "function_call" || typ == "mcp_tool_call") && (name == "AskUserQuestion" || name == "request_user_input" || strings.HasSuffix(name, "__request_user_input")) {
			return true
		}
		if str(v["method"]) == "item/tool/requestUserInput" {
			return true
		}
		for _, key := range []string{"message", "content", "item", "delta", "content_block", "params"} {
			if humanTool(v[key]) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if humanTool(item) {
				return true
			}
		}
	}
	return false
}
func str(v any) string { s, _ := v.(string); return s }
func Merge(a, b int) int {
	if a == NeedsHuman || b == NeedsHuman {
		return NeedsHuman
	}
	if a == ColdStart || b == ColdStart {
		return ColdStart
	}
	if a == Retryable || b == Retryable {
		return Retryable
	}
	return 0
}
