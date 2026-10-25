package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

type notifyCollector struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (c *notifyCollector) add(body map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
}

func (c *notifyCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *notifyCollector) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return nil
	}
	return c.bodies[len(c.bodies)-1]
}

func newWebhookServer(t *testing.T) (*httptest.Server, *notifyCollector) {
	t.Helper()
	collector := &notifyCollector{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		collector.add(body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv, collector
}

func isolateNotifyHomes(t *testing.T) (root, claudeHome, codexHome, fakeUserHome string) {
	t.Helper()
	root = t.TempDir()
	claudeHome = t.TempDir()
	codexHome = t.TempDir()
	fakeUserHome = t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", codexHome)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", fakeUserHome)
	} else {
		t.Setenv("HOME", fakeUserHome)
	}
	return root, claudeHome, codexHome, fakeUserHome
}

func TestNotifySetTestInstallHookUninstall(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root, claudeHome, codexHome, fakeUserHome := isolateNotifyHomes(t)
	srv, collector := newWebhookServer(t)

	if _, err := command(t, root, "tok-webhook", "notify", "set", "--type", "webhook", "--endpoint", srv.URL, "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	// 令牌绝不能出现在任何输出里。
	channelRaw, _ := os.ReadFile(filepath.Join(root, "notify-channel.json"))
	if strings.Contains(string(channelRaw), "tok-webhook") {
		t.Fatal("通道文件明文包含令牌")
	}

	if _, err := command(t, root, "", "notify", "test"); err != nil {
		t.Fatal(err)
	}
	if collector.count() != 1 {
		t.Fatalf("测试通知应送达 1 次: %d", collector.count())
	}
	if body := collector.last(); body["type"] != "task_completed" || body["title"] != "任务完成" {
		t.Fatalf("测试通知载荷不符: %#v", body)
	}

	out, err := command(t, root, "", "notify", "install")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"chained_previous_notify": false`) {
		t.Fatalf("安装报告不符: %s", out)
	}
	claudeSettings := filepath.Join(claudeHome, "settings.json")
	raw, err := os.ReadFile(claudeSettings)
	if err != nil || !strings.Contains(string(raw), "notify hook claude") {
		t.Fatalf("Claude settings.json 未写入 hook: %v", err)
	}
	codexRaw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var codexTop map[string]any
	if err := toml.Unmarshal(codexRaw, &codexTop); err != nil {
		t.Fatal(err)
	}
	if codexArgs := stringArgs(t, codexTop["notify"]); !strings.HasSuffix(strings.Join(codexArgs, " "), "notify hook codex") {
		t.Fatalf("Codex notify 不符: %#v", codexArgs)
	}
	zcodeRaw, err := os.ReadFile(filepath.Join(fakeUserHome, ".zcode", "cli", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var zcodeObject struct {
		Hooks struct {
			Enabled bool             `json:"enabled"`
			Events  map[string][]any `json:"events"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(zcodeRaw, &zcodeObject); err != nil {
		t.Fatal(err)
	}
	if !zcodeObject.Hooks.Enabled || zcodeObject.Hooks.Events["Stop"] == nil || zcodeObject.Hooks.Events["PermissionRequest"] == nil || zcodeObject.Hooks.Events["PreToolUse"] == nil {
		t.Fatalf("ZCode hooks 不符: %s", zcodeRaw)
	}

	status, err := command(t, root, "", "notify", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"installed": true`, `"configured": true`, `"type": "webhook"`} {
		if !strings.Contains(status, want) {
			t.Fatalf("status 缺少 %s: %s", want, status)
		}
	}

	// hook 路径：无 stdout、退出码 0、送达一次、10 秒窗口内去重。
	hookOut, err := command(t, root, `{"hook_event_name":"Stop","session_id":"s-1","cwd":"/tmp/demo"}`, "notify", "hook", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if hookOut != "" {
		t.Fatalf("hook 命令不应写 stdout: %q", hookOut)
	}
	if _, err := command(t, root, `{"hook_event_name":"Stop","session_id":"s-1","cwd":"/tmp/demo"}`, "notify", "hook", "claude"); err != nil {
		t.Fatal(err)
	}
	if collector.count() != 2 {
		t.Fatalf("去重失效，应只送 1 次 hook 通知: %d", collector.count())
	}
	if body := collector.last(); body["project"] != "demo" || body["cli"] != "claude" {
		t.Fatalf("hook 通知载荷不符: %#v", body)
	}

	// Codex 载荷走 argv。
	if _, err := command(t, root, "", "notify", "hook", "codex", `{"type":"approval-requested","thread_id":"t-9"}`); err != nil {
		t.Fatal(err)
	}
	if collector.count() != 3 {
		t.Fatalf("Codex hook 应送达: %d", collector.count())
	}
	if body := collector.last(); body["type"] != "needs_human" {
		t.Fatalf("approval-requested 应映射为需要人工: %#v", body)
	}

	if _, err := command(t, root, "", "notify", "uninstall"); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(claudeSettings); err != nil || strings.Contains(string(raw), "notify hook claude") || strings.Contains(string(raw), "\"hooks\"") {
		t.Fatalf("卸载后 Claude hooks 未清干净: %s %v", raw, err)
	}
	codexRaw, err = os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	codexTop = map[string]any{}
	if err := toml.Unmarshal(codexRaw, &codexTop); err != nil {
		t.Fatal(err)
	}
	if _, exists := codexTop["notify"]; exists {
		t.Fatalf("卸载后 Codex notify 未移除: %s", codexRaw)
	}
	zcodeAfter, err := os.ReadFile(filepath.Join(fakeUserHome, ".zcode", "cli", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var zcodeEmpty map[string]any
	if err := json.Unmarshal(zcodeAfter, &zcodeEmpty); err != nil {
		t.Fatal(err)
	}
	if _, exists := zcodeEmpty["hooks"]; exists {
		t.Fatalf("卸载后 ZCode hooks 未还原: %s", zcodeAfter)
	}
}

func stringArgs(t *testing.T, raw any) []string {
	t.Helper()
	array, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(array))
	for _, item := range array {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

func TestNotifyHookSilentWhenVaultLocked(t *testing.T) {
	// 有通道但密钥库无法解锁（口令错误且非交互）：hook 必须静默成功并记录日志。
	root, _, _, _ := isolateNotifyHomes(t)
	t.Setenv("RELAY_PASSPHRASE", "first-passphrase-long-enough")
	if _, err := command(t, root, "tok-webhook", "notify", "set", "--type", "webhook", "--endpoint", "http://127.0.0.1:1", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_PASSPHRASE", "second-passphrase-long-enough")
	out, err := command(t, root, `{"hook_event_name":"Stop","session_id":"s-2"}`, "notify", "hook", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("hook 不应产生输出: %q", out)
	}
	logRaw, err := os.ReadFile(filepath.Join(root, "notify.log"))
	if err != nil || !strings.Contains(string(logRaw), "密钥库") {
		t.Fatalf("失败应记录到 notify.log: %v %s", err, logRaw)
	}
}

func TestNotifySetRequiresChannelFields(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	root, _, _, _ := isolateNotifyHomes(t)
	if _, err := command(t, root, "", "notify", "set", "--type", "bark"); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("bark 缺 key 应报错: %v", err)
	}
	if _, err := command(t, root, "", "notify", "set", "--type", "unknown"); err == nil {
		t.Fatal("未知类型应报错")
	}
	if _, err := command(t, root, "", "notify", "test"); err == nil || !strings.Contains(err.Error(), "尚未配置") {
		t.Fatalf("未配置通道时 test 应报错: %v", err)
	}
}
