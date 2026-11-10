package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type captured struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

func newCaptureServer(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		got.Method, got.Path, got.Header, got.Body = r.Method, r.URL.Path, r.Header.Clone(), body
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func sampleEvent() NotificationEvent {
	return NotificationEvent{
		Type: "task_completed",
		Payload: map[string]any{
			"title":   "任务完成",
			"body":    "host-1 · proj · claude",
			"cli":     "claude",
			"host":    "host-1",
			"project": "proj",
			"session": "abcd1234",
		},
	}
}

func TestWebhookNotifier(t *testing.T) {
	srv, got := newCaptureServer(t, 200)
	notifier, err := NewWebhook(srv.URL, "tok-webhook")
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sampleEvent()); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.Path != "/" {
		t.Fatalf("请求形态不符: %s %s", got.Method, got.Path)
	}
	if got.Header.Get("Authorization") != "Bearer tok-webhook" {
		t.Fatal("Bearer 令牌未随请求发送")
	}
	if got.Body["type"] != "task_completed" || got.Body["title"] != "任务完成" || got.Body["project"] != "proj" {
		t.Fatalf("webhook 载荷字段缺失: %#v", got.Body)
	}
	if _, err := NewWebhook("", ""); err == nil {
		t.Fatal("缺少 URL 应报错")
	}
}

func TestBarkNotifier(t *testing.T) {
	srv, got := newCaptureServer(t, 200)
	notifier, err := NewBark(srv.URL, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sampleEvent()); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/push" {
		t.Fatalf("Bark 应 POST 到 /push: %s", got.Path)
	}
	// Bark 约定标题与正文必须分开传递。
	if got.Body["device_key"] != "key-1" || got.Body["title"] != "任务完成" || got.Body["body"] != "host-1 · proj · claude" || got.Body["group"] != "relay" {
		t.Fatalf("Bark 载荷不符: %#v", got.Body)
	}
	if _, err := NewBark(srv.URL, ""); err == nil {
		t.Fatal("缺少设备 key 应报错")
	}
}

func TestTelegramNotifier(t *testing.T) {
	srv, got := newCaptureServer(t, 200)
	notifier, err := NewTelegram(srv.URL, "bot-token-1", "chat-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sampleEvent()); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/botbot-token-1/sendMessage" {
		t.Fatalf("Telegram 路径不符: %s", got.Path)
	}
	text, _ := got.Body["text"].(string)
	if got.Body["chat_id"] != "chat-9" || !strings.Contains(text, "任务完成") || !strings.Contains(text, "host-1 · proj · claude") {
		t.Fatalf("Telegram 载荷不符: %#v", got.Body)
	}
	if _, err := NewTelegram(srv.URL, "", "chat-9"); err == nil {
		t.Fatal("缺少 bot token 应报错")
	}
	if _, err := NewTelegram(srv.URL, "tok", ""); err == nil {
		t.Fatal("缺少 chat id 应报错")
	}
}

func TestNtfyNotifier(t *testing.T) {
	srv, got := newCaptureServer(t, 200)
	notifier, err := NewNtfy(srv.URL, "relay-topic", "tk_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sampleEvent()); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/" || got.Header.Get("Authorization") != "Bearer tk_1" {
		t.Fatalf("ntfy 请求形态不符: %s %v", got.Path, got.Header)
	}
	if got.Body["topic"] != "relay-topic" || got.Body["title"] != "任务完成" || got.Body["message"] != "host-1 · proj · claude" {
		t.Fatalf("ntfy 载荷不符: %#v", got.Body)
	}
	if _, err := NewNtfy(srv.URL, "", ""); err == nil {
		t.Fatal("缺少 topic 应报错")
	}
}

func TestNotifierServerError(t *testing.T) {
	srv, _ := newCaptureServer(t, 500)
	notifier, err := NewWebhook(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Notify(context.Background(), sampleEvent()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("服务端错误应返回失败: %v", err)
	}
}
