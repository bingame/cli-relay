package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
)

func TestLocalCodexResumeAcrossProviders(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	if os.Getenv("RELAY_CODEX_INTEGRATION") != "1" {
		t.Skip("设置 RELAY_CODEX_INTEGRATION=1 才运行本机隔离恢复测试")
	}
	binary := integrationBinary(t)
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	a := New()
	a.Binary, a.NativeHome = binary, home
	var receivedA, receivedB, receivedHistory atomic.Bool
	server := func(model, key string, received *atomic.Bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			var request map[string]any
			if json.Unmarshal(body, &request) != nil || request["model"] != model || r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, "unexpected model or credential", http.StatusBadRequest)
				return
			}
			received.Store(true)
			if model == "relay-model-b" && bytes.Contains(body, []byte("RELAY_HISTORY_FROM_A")) && bytes.Contains(body, []byte("RELAY_CONTINUE_WITH_B")) && bytes.Contains(body, []byte("RELAY_LOCAL_PROBE_OK")) {
				receivedHistory.Store(true)
			}
			writeProbeResponse(w)
		}))
	}
	serverA := server("relay-model-a", "fake-key-a", &receivedA)
	defer serverA.Close()
	serverB := server("relay-model-b", "fake-key-b", &receivedB)
	defer serverB.Close()
	launch := func(id, model, url, key string) adapter.LaunchInputs {
		p := sampleProvider()
		p.ID, p.Model, p.BaseURL, p.SecretMode = id, model, url+"/v1", "env_key"
		p.Extra = map[string]any{"codex_config": map[string]any{"model_provider": "original", "model_providers": map[string]any{"original": map[string]any{"request_max_retries": 0, "stream_max_retries": 0}}}}
		artifact, err := a.Render(p, filepath.Join(root, "relay-home"))
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := a.BuildLaunchInputs(artifact, adapter.ResolvedSecrets{"api_key": key})
		if err != nil {
			t.Fatal(err)
		}
		return inputs
	}
	inputsA := launch("provider-a", "relay-model-a", serverA.URL, "fake-key-a")
	inputsB := launch("provider-b", "relay-model-b", serverB.URL, "fake-key-b")
	run := func(inputs adapter.LaunchInputs, proxy string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		commandArgs := append(append([]string{}, inputs.Args...), args...)
		output, err := integrationCommand(ctx, binary, commandArgs, root, home, proxy, inputs.Env).CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("turn.completed")) {
			safe := strings.ReplaceAll(strings.ReplaceAll(string(output), "fake-key-a", "[已移除虚构凭据]"), "fake-key-b", "[已移除虚构凭据]")
			t.Fatalf("本地会话执行失败：%v\n%s", err, safe)
		}
		return output
	}
	output := run(inputsA, serverA.URL, "exec", "--skip-git-repo-check", "--json", "RELAY_HISTORY_FROM_A")
	var threadID string
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "thread.started" {
			threadID = event.ThreadID
		}
	}
	if threadID == "" || !receivedA.Load() {
		t.Fatal("未获得供应商 A 的持久会话")
	}
	rollouts, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	foundCustom := false
	for _, path := range rollouts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			var record struct {
				Type    string         `json:"type"`
				Payload map[string]any `json:"payload"`
			}
			if json.Unmarshal(line, &record) == nil && record.Type == "session_meta" && record.Payload["id"] == threadID {
				foundCustom = record.Payload["model_provider"] == nativeProviderID
			}
		}
	}
	if !foundCustom {
		t.Fatal("持久会话未记录 model_provider=custom")
	}
	// 额外验证 app-server 的 thread/resume 能读取统一身份的持久会话。
	{
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		// 独立 CODEX_HOME 同样支持直接加载 app-server，用于检查共享会话发现/恢复。
		args := append([]string{}, inputsB.Args...)
		args = append(args, "-c", "model_provider='custom'", "-c", "model='relay-model-b'")
		args = append(args, "app-server", "--listen", "stdio://")
		cmd := integrationCommand(ctx, binary, args, root, home, serverB.URL, inputsB.Env)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(stdin)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		reply := func(id int, method string, params any) map[string]any {
			t.Helper()
			if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
				t.Fatal(err)
			}
			for scanner.Scan() {
				var message map[string]any
				if json.Unmarshal(scanner.Bytes(), &message) == nil && message["id"] == float64(id) {
					return message
				}
			}
			t.Fatalf("未收到 %s RPC 响应：%v", method, scanner.Err())
			return nil
		}
		func() {
			defer func() { stdin.Close(); cancel(); cmd.Wait() }()
			initialized := reply(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "relay_test", "version": "0.0.0"}})
			if initialized["error"] != nil {
				t.Fatalf("RPC 初始化失败：%v", initialized["error"])
			}
			if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
				t.Fatal(err)
			}
			listed := reply(20, "thread/list", map[string]any{"modelProviders": []string{"custom"}, "sourceKinds": []string{"exec"}, "limit": 100})
			encoded, _ := json.Marshal(listed)
			if listed["error"] != nil || !bytes.Contains(encoded, []byte(threadID)) {
				t.Fatalf("供应商 B 未发现共享会话: %s", encoded)
			}
			resumed := reply(2, "thread/resume", map[string]any{"threadId": threadID, "modelProvider": nativeProviderID})
			if resumed["error"] != nil || resumed["result"] == nil {
				t.Fatalf("统一 custom 后恢复失败：%v", resumed["error"])
			}
		}()
	}
	run(inputsB, serverB.URL, "exec", "resume", "--skip-git-repo-check", "--json", threadID, "RELAY_CONTINUE_WITH_B")
	if !receivedB.Load() || !receivedHistory.Load() {
		t.Fatal("供应商 B 未收到自身凭据、模型以及 A 的历史")
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("临时跨供应商恢复不应创建主配置")
	}
	t.Log("A 创建会话，B 经 thread/resume 和 exec resume 恢复，并用自身凭据续接历史")
}
