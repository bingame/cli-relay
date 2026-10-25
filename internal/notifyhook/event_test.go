package notifyhook

import (
	"strings"
	"testing"
)

func TestParseStdinEventClaude(t *testing.T) {
	cases := []struct {
		name         string
		payload      string
		wantKind     string
		wantSkip     bool
		wantMsg      string
		checkProject string
	}{
		{
			name:         "Stop 完成事件",
			payload:      `{"hook_event_name":"Stop","session_id":"sess-1","cwd":"/home/u/proj"}`,
			wantKind:     KindTaskCompleted,
			checkProject: "proj",
		},
		{
			name:     "Stop 续跑不重复通知",
			payload:  `{"hook_event_name":"Stop","stop_hook_active":true}`,
			wantSkip: true,
		},
		{
			name:     "Notification 等待人工",
			payload:  `{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash","cwd":"/p"}`,
			wantKind: KindNeedsHuman,
			wantMsg:  "Claude needs your permission to use Bash",
		},
		{
			name:     "AskUserQuestion 提取问题",
			payload:  `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"header":"通道","question":"用哪个通道？"}]},"cwd":"/p"}`,
			wantKind: KindNeedsHuman,
			wantMsg:  "用哪个通道？",
		},
		{
			name:     "ExitPlanMode 提取计划",
			payload:  `{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"第一步做 A"}}`,
			wantKind: KindNeedsHuman,
			wantMsg:  "第一步做 A",
		},
		{
			name:     "其他工具不通知",
			payload:  `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			wantSkip: true,
		},
		{
			name:     "未安装的事件忽略",
			payload:  `{"hook_event_name":"SessionStart","source":"startup"}`,
			wantSkip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseStdinEvent("claude", []byte(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Skip != tc.wantSkip {
				t.Fatalf("Skip=%v, 期望 %v", ev.Skip, tc.wantSkip)
			}
			if !tc.wantSkip {
				if ev.Kind != tc.wantKind {
					t.Fatalf("Kind=%s, 期望 %s", ev.Kind, tc.wantKind)
				}
				if tc.wantMsg != "" && ev.Message != tc.wantMsg {
					t.Fatalf("Message=%q, 期望 %q", ev.Message, tc.wantMsg)
				}
			}
			if tc.checkProject != "" && ev.Project != tc.checkProject {
				t.Fatalf("Project=%s, 期望 %s", ev.Project, tc.checkProject)
			}
		})
	}
	if _, err := ParseStdinEvent("claude", []byte("not-json")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestParseStdinEventZcode(t *testing.T) {
	ev, err := ParseStdinEvent("zcode", []byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"s9"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindNeedsHuman || ev.Message != "Bash" {
		t.Fatalf("PermissionRequest 应映射为需要人工: %#v", ev)
	}
	ev, err = ParseStdinEvent("zcode", []byte(`{"hook_event_name":"Stop","session_id":"s9"}`))
	if err != nil || ev.Kind != KindTaskCompleted {
		t.Fatalf("Stop 应映射为任务完成: %#v %v", ev, err)
	}
}

func TestParseCodexEvent(t *testing.T) {
	ev, err := ParseCodexEvent(`{"type":"agent-turn-complete","thread_id":"th-1","last_assistant_message":"已完成重构"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindTaskCompleted || ev.CLI != "codex" || ev.Session != "th-1" || ev.Message != "已完成重构" {
		t.Fatalf("agent-turn-complete 映射不符: %#v", ev)
	}
	ev, err = ParseCodexEvent(`{"type":"approval-requested","thread_id":"th-2"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != KindNeedsHuman {
		t.Fatalf("approval-requested 应映射为需要人工: %#v", ev)
	}
	ev, err = ParseCodexEvent(`{"type":"something-else"}`)
	if err != nil || !ev.Skip {
		t.Fatalf("未知事件应跳过: %#v %v", ev, err)
	}
	if _, err := ParseCodexEvent("{bad"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("很", 300)
	got := truncate(long, 100)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 101 {
		t.Fatalf("截断结果不符: %d 个字符", len([]rune(got)))
	}
	if truncate("  短消息  ", 10) != "短消息" {
		t.Fatal("两端空白应被清理")
	}
}
