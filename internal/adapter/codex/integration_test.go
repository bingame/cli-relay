package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
)

// 显式开启才调用本机 Codex；模型请求只访问 127.0.0.1 假 SSE 服务。
// PowerShell: $env:RELAY_CODEX_INTEGRATION='1'; go test ./internal/adapter/codex -run TestLocalCodexIntegration -v
func TestLocalCodexIntegration(t *testing.T) {
	if os.Getenv("RELAY_CODEX_INTEGRATION") != "1" {
		t.Skip("设置 RELAY_CODEX_INTEGRATION=1 才运行本机隔离集成测试")
	}
	binary := integrationBinary(t)
	for _, testCase := range []string{"override", "profile", "global-switch-with-old-default-profile"} {
		t.Run(testCase, func(t *testing.T) {
			var authenticated atomic.Bool
			var receivedModel atomic.Bool
			const dummyKey = "relay-local-fake-credential"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
					http.NotFound(w, r)
					return
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					http.Error(w, "read failed", 400)
					return
				}
				var request map[string]any
				if json.Unmarshal(body, &request) == nil && request["model"] == "relay-local-test-model" {
					receivedModel.Store(true)
				}
				if r.Header.Get("Authorization") == "Bearer "+dummyKey {
					authenticated.Store(true)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				message := map[string]any{"id": "msg_relay_test", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "RELAY_LOCAL_PROBE_OK", "annotations": []any{}}}}
				response := map[string]any{"id": "resp_relay_test", "object": "response", "status": "completed", "output": []any{message}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
				events := []map[string]any{
					{"type": "response.created", "response": map[string]any{"id": "resp_relay_test", "object": "response", "status": "in_progress", "output": []any{}}},
					{"type": "response.output_item.added", "output_index": 0, "item": message},
					{"type": "response.output_text.delta", "item_id": "msg_relay_test", "output_index": 0, "content_index": 0, "delta": "RELAY_LOCAL_PROBE_OK"},
					{"type": "response.output_item.done", "output_index": 0, "item": message},
					{"type": "response.completed", "response": response},
				}
				for _, event := range events {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			home := filepath.Join(root, "codex-home")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			// 证明从模型 shell 环境排除鉴权变量，不妨碍 Codex 自身读取 env_key。
			config := "[shell_environment_policy]\nfilters = { 'RELAY_*' = 'exclude' }\n"
			if testCase == "global-switch-with-old-default-profile" {
				config = "profile = 'old-manual' # 原来的默认 profile\nmodel = 'old-model'\nmodel_provider = 'openai'\n" + config + "\n[profiles.historical]\nmodel = 'historical-model'\n"
				if err := os.WriteFile(filepath.Join(home, "old-manual.config.toml"), []byte("model='old-profile-model'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			p := sampleProvider()
			p.ID, p.BaseURL, p.Model = "local-integration", server.URL+"/v1", "relay-local-test-model"
			p.Extra = map[string]any{"codex_config": map[string]any{"model_provider": "test", "model_providers": map[string]any{"test": map[string]any{"request_max_retries": 0, "stream_max_retries": 0}}}}
			// 模拟 Provider 经 SQLite JSON 列往返，覆盖整数被标准 JSON 解码成 float64 的路径。
			stored, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(stored, &p); err != nil {
				t.Fatal(err)
			}
			a := New()
			a.Binary, a.NativeHome = binary, home
			artifact, err := a.Render(p, filepath.Join(root, "relay-home"))
			if err != nil {
				t.Fatal(err)
			}
			if testCase != "override" {
				if err := a.ApplyGlobal(artifact, home); err != nil {
					t.Fatal(err)
				}
				if testCase == "profile" {
					a.LaunchMode = "profile"
				}
			}
			inputs, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": dummyKey})
			if err != nil {
				t.Fatal(err)
			}
			args, err := a.NativeArgs(adapter.Headless, []string{"--skip-git-repo-check", "--ephemeral", "Reply RELAY_LOCAL_PROBE_OK without tools."})
			if err != nil {
				t.Fatal(err)
			}
			if testCase != "global-switch-with-old-default-profile" {
				args = append(inputs.Args, args...)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, inputs.Binary, args...)
			cmd.Dir = root
			// 从白名单构造宿主环境，隔离用户配置与真实凭据。
			for _, key := range []string{"PATH", "SystemRoot", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT"} {
				if value := os.Getenv(key); value != "" {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+root, "USERPROFILE="+root, "APPDATA="+root, "LOCALAPPDATA="+root, "TEMP="+root, "TMP="+root, "CODEX_HOME="+home, "NO_COLOR=1")
			for key, value := range inputs.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if err != nil || !authenticated.Load() || !receivedModel.Load() || !strings.Contains(string(output), "turn.completed") || !strings.Contains(string(output), "RELAY_LOCAL_PROBE_OK") {
				safe := strings.ReplaceAll(string(output), dummyKey, "[已移除虚构凭据]")
				if len(safe) > 5000 {
					safe = safe[len(safe)-5000:]
				}
				t.Fatalf("本机 Codex 探针失败：err=%v，收到正确鉴权=%v，收到正确模型=%v\n%s", err, authenticated.Load(), receivedModel.Load(), safe)
			}
			t.Log("真实适配器参数已被 Codex 接受；本地假服务收到正确鉴权和模型，输出 turn.completed")
		})
	}
}

func integrationBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("RELAY_CODEX_BINARY"); binary != "" {
		absolute, err := filepath.Abs(binary)
		if err != nil {
			t.Fatal(err)
		}
		return absolute
	}
	if runtime.GOOS == "windows" {
		root := filepath.Join(os.Getenv("APPDATA"), "npm", "node_modules", "@openai")
		var candidate string
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.EqualFold(entry.Name(), "codex.exe") {
				candidate = path
				return filepath.SkipAll
			}
			return nil
		})
		if candidate != "" {
			return candidate
		}
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("未找到 Codex；可用 RELAY_CODEX_BINARY 指定本机原生二进制")
	}
	return binary
}
