package classify

import "testing"

func TestStructuredErrorsAndHumanInput(t *testing.T) {
	cases := []struct {
		line   string
		code   int
		failed bool
	}{
		{`{"type":"error","message":"stream disconnected"}`, 10, true},
		{`{"type":"turn.failed","error":{"message":"ResumeRejected"}}`, 12, true},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion"}]}}`, 11, false},
		{`{"type":"item.completed","item":{"type":"agent_message","text":"文档说明 AskUserQuestion 和 stream disconnected"}}`, 0, false},
		{`{"type":"result","subtype":"error_during_execution","result":"unknown failure"}`, 0, true},
	}
	for _, c := range cases {
		e := Parse([]byte(c.line))
		if e.Code != c.code || e.Failed != c.failed {
			t.Fatalf("%s: %+v", c.line, e)
		}
	}
}
