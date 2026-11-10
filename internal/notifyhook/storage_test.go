package notifyhook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/secrets"
)

func testVault(t *testing.T) *secrets.Vault {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	vault, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func otherVault(t *testing.T) *secrets.Vault {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(255 - i)
	}
	vault, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func TestChannelRoundtripAndClear(t *testing.T) {
	home := t.TempDir()
	vault := testVault(t)
	channel := Channel{Type: "bark", Endpoint: "https://api.day.app", Token: "key-1"}
	if err := SaveChannel(home, vault, channel); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "notify-channel.json"))
	if strings.Contains(string(raw), "key-1") {
		t.Fatal("令牌不能明文落盘")
	}
	// 用另一把密钥解不开，确认确实加密。
	if _, _, err := LoadChannel(home, otherVault(t)); err == nil {
		t.Fatal("错误密钥不应解开通道记录")
	}
	loaded, ok, err := LoadChannel(home, vault)
	if err != nil || !ok || loaded != channel {
		t.Fatalf("通道往返不符: %#v %v %v", loaded, ok, err)
	}
	cleared, err := ClearChannel(home)
	if err != nil || !cleared {
		t.Fatalf("清除通道失败: %v %v", cleared, err)
	}
	if _, ok, _ := LoadChannel(home, vault); ok {
		t.Fatal("清除后不应再能读取")
	}
	if cleared, err := ClearChannel(home); err != nil || cleared {
		t.Fatalf("重复清除应无操作: %v %v", cleared, err)
	}
}

func TestChannelValidation(t *testing.T) {
	vault := testVault(t)
	home := t.TempDir()
	for _, channel := range []Channel{
		{Type: "webhook"},
		{Type: "bark"},
		{Type: "telegram", Token: "t"},
		{Type: "ntfy"},
		{Type: "unknown"},
	} {
		if err := SaveChannel(home, vault, channel); err == nil {
			t.Fatalf(" %#v 应被拒绝", channel)
		}
	}
}

func TestAllowOnceDedup(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	if !AllowOnce(home, "claude|s1|task_completed", now) {
		t.Fatal("首次应放行")
	}
	if AllowOnce(home, "claude|s1|task_completed", now.Add(2*time.Second)) {
		t.Fatal("窗口内应去重")
	}
	if !AllowOnce(home, "claude|s1|task_completed", now.Add(11*time.Second)) {
		t.Fatal("窗口外应再次放行")
	}
	if !AllowOnce(home, "claude|s2|task_completed", now) {
		t.Fatal("不同 session 互不影响")
	}
}

func TestLogErrorAndTail(t *testing.T) {
	home := t.TempDir()
	LogError(home, "事件一: %s", "失败")
	LogError(home, "事件二: 失败")
	tail := LogTail(home, 2048)
	if !strings.Contains(tail, "事件一") || !strings.Contains(tail, "事件二") {
		t.Fatalf("日志内容不符: %q", tail)
	}
}

func TestBuildNotifierUnknownType(t *testing.T) {
	if _, err := BuildNotifier(Channel{Type: "nope"}); err == nil {
		t.Fatal("未知类型应报错")
	}
}

func TestSendWithoutChannel(t *testing.T) {
	err := Send(context.Background(), t.TempDir(), testVault(t), Event{Kind: KindNeedsHuman})
	if !IsNoChannel(err) {
		t.Fatalf("未配置通道应返回可识别错误: %v", err)
	}
}

// 用 helper 进程验证 Codex 链式转发：载荷作为最后一个参数原样传给原程序。
func TestForwardPreviousRunsProgram(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RELAY_NOTIFY_HELPER", dir)
	defer os.Unsetenv("RELAY_NOTIFY_HELPER")
	ForwardPrevious(t.TempDir(), []string{os.Args[0], "-test.run=TestForwardHelper", "--", dir}, `{"type":"agent-turn-complete"}`)
	marker := filepath.Join(dir, "forwarded.txt")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != `{"type":"agent-turn-complete"}` {
		t.Fatalf("转发载荷不符: %q", raw)
	}
	// 无程序时不应 panic。
	ForwardPrevious(t.TempDir(), nil, "{}")
}

func TestForwardHelper(t *testing.T) {
	dir := os.Getenv("RELAY_NOTIFY_HELPER")
	if dir == "" {
		t.Skip("仅由 TestForwardPreviousRunsProgram 子进程调用")
	}
	payload := os.Args[len(os.Args)-1]
	if err := os.WriteFile(filepath.Join(dir, "forwarded.txt"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
}
