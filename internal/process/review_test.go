package process

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/classify"
)

func TestReviewRPCFrameFlushesBeforeNextRequest(t *testing.T) {
	var forwarded bytes.Buffer
	s := newStream(&forwarded, []string{"relay-fake-long-secret-for-review"}, nil)
	frame := "{\"id\":1,\"result\":{\"userAgent\":\"test\"}}\n"
	if _, err := s.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	if forwarded.String() != frame {
		t.Fatal("完整 RPC 响应在下一请求前必须转发，否则父子进程互相等待")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReviewJSONEscapedCredentialIsHiddenAcrossChunks(t *testing.T) {
	for _, credential := range []string{`fake-"credential\value`, `fake<&>credential`, `\fake-credential`, `"fake-credential`} {
		for _, escapeHTML := range []bool{true, false} {
			var encoded bytes.Buffer
			encoder := json.NewEncoder(&encoded)
			encoder.SetEscapeHTML(escapeHTML)
			if err := encoder.Encode(map[string]any{"type": "result", "result": "answer " + credential}); err != nil {
				t.Fatal(err)
			}
			raw := encoded.Bytes()
			var forwarded bytes.Buffer
			s := newStream(&forwarded, []string{credential}, nil)
			for start := 0; start < len(raw); start += 3 {
				end := start + 3
				if end > len(raw) {
					end = len(raw)
				}
				if _, err := s.Write(raw[start:end]); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(forwarded.Bytes(), &result); err != nil {
				t.Fatalf("脱敏破坏 JSON 协议: %v", err)
			}
			if strings.Contains(result["result"].(string), credential) {
				t.Fatal("原始密钥的 JSON 转义形式穿过分片脱敏边界")
			}
		}
	}
}

func TestReviewAppServerSessionFrames(t *testing.T) {
	for _, frame := range []string{
		`{"method":"thread/started","params":{"thread":{"id":"review-thread"}}}`,
		`{"method":"turn/started","params":{"threadId":"review-thread","turn":{"id":"turn-1","status":"inProgress"}}}`,
		`{"method":"turn/completed","params":{"threadId":"review-thread","turn":{"id":"turn-1","status":"completed"}}}`,
	} {
		event := classify.Parse([]byte(frame))
		if event.SessionID != "review-thread" {
			t.Fatal("app-server 标准通知缺失会话元数据")
		}
	}
}
