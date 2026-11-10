package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// 通过真实启动环境验证 config/batchWrite；覆盖旧 false 失败对照和新隔离配置。
func TestLocalCodexModelSelectionPersistence(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	if os.Getenv("RELAY_CODEX_INTEGRATION") != "1" {
		t.Skip("设置 RELAY_CODEX_INTEGRATION=1 才运行本机隔离配置保存测试")
	}
	binary := integrationBinary(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	for _, mode := range []string{"legacy-reset-rejected", "scoped-callback", "scoped-env", "global-switch-saves-default"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "codex-home")
			a := New()
			a.Binary, a.NativeHome = binary, home
			p := sampleProvider()
			p.BaseURL = server.URL + "/v1"
			if mode == "scoped-env" {
				p.SecretMode = "env_key"
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			original := "model_provider='custom'\n[model_providers.custom]\nname='base'\nenv_key='OLD_KEY'\n"
			if mode == "scoped-env" {
				original = "model_provider='custom'\n[model_providers.custom]\nname='base'\n[model_providers.custom.auth]\ncommand='must-not-run'\n"
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			skillDir := filepath.Join(home, "skills", "relay-persistence-probe")
			if err := os.MkdirAll(skillDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: relay-persistence-probe\ndescription: Isolated test skill\n---\nTest only.\n"), 0600); err != nil {
				t.Fatal(err)
			}

			artifact, err := a.Render(p, filepath.Join(root, "relay-home"))
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := a.BuildLaunchInputs(artifact, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(inputs.Env["CODEX_HOME"], "config.toml")
			args := []string{"app-server", "--listen", "stdio://"}
			if mode == "legacy-reset-rejected" {
				// 固定旧实现，不从新适配器取布尔占位。
				if err := os.WriteFile(path, []byte("model='"+p.Model+"'\nmodel_provider='custom'\n[model_providers]\ncustom=false\n"), 0600); err != nil {
					t.Fatal(err)
				}
				encoded, err := inlineTOML(artifact.Config["model_providers"].(map[string]any)[nativeProviderID])
				if err != nil {
					t.Fatal(err)
				}
				args = append([]string{"-c", "model_providers.custom=" + encoded}, args...)
			} else if mode == "global-switch-saves-default" {
				if err := a.ApplyGlobal(artifact, home); err != nil {
					t.Fatal(err)
				}
				inputs.Env["CODEX_HOME"] = home
				path = filepath.Join(home, "config.toml")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := integrationCommand(ctx, binary, args, root, home, server.URL, inputs.Env)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				stdin.Close()
				cancel()
				cmd.Wait()
			}()
			encoder := json.NewEncoder(stdin)
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 4096), 8<<20)
			rpc := func(id int, method string, params any) map[string]any {
				t.Helper()
				if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
					t.Fatal(err)
				}
				for scanner.Scan() {
					var response map[string]any
					if json.Unmarshal(scanner.Bytes(), &response) == nil && response["id"] == float64(id) {
						return response
					}
				}
				t.Fatalf("未收到 %s 响应：%v", method, scanner.Err())
				return nil
			}
			initialized := rpc(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "relay_config_write_test", "version": "0.0.0"}})
			if initialized["error"] != nil {
				t.Fatalf("初始化失败：%v", initialized["error"])
			}
			if err := encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
				t.Fatal(err)
			}
			read := rpc(2, "config/read", map[string]any{"includeLayers": false})
			if read["error"] != nil {
				t.Fatalf("启动配置无效：%v", read["error"])
			}
			config := read["result"].(map[string]any)["config"].(map[string]any)
			if config["model_provider"] != nativeProviderID || config["model"] != p.Model {
				t.Fatal("启动配置没有使用预期供应商与模型")
			}
			if strings.HasPrefix(mode, "scoped-") {
				skills := rpc(20, "skills/list", map[string]any{"cwds": []string{root}, "forceReload": true})
				encoded, _ := json.Marshal(skills)
				if skills["error"] != nil || !bytes.Contains(encoded, []byte("relay-persistence-probe")) {
					t.Fatal("独立配置中未发现共享 Skill")
				}
			}

			write := rpc(3, "config/batchWrite", map[string]any{
				"edits": []any{
					map[string]any{"keyPath": "model", "value": "relay-selected-model", "mergeStrategy": "replace"},
					map[string]any{"keyPath": "model_reasoning_effort", "value": "high", "mergeStrategy": "replace"},
				},
				"reloadUserConfig": true,
			})
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "legacy-reset-rejected" {
				failure, ok := write["error"].(map[string]any)
				if !ok {
					t.Fatal("上游已接受重置层，请重新评估此兼容限制")
				}
				message, _ := failure["message"].(string)
				data, _ := failure["data"].(map[string]any)
				if failure["code"] != float64(-32600) || data["config_write_error_code"] != "configValidationError" || !strings.Contains(message, "boolean `false`") || !strings.Contains(message, "model_providers.custom") {
					t.Fatalf("未复现预期的配置保存错误：%v", failure)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("拒绝保存时修改了用户层")
				}
				t.Log("有效的启动配置仍在保存时因用户层 custom=false 被拒绝；文件未改变")
				return
			}
			if write["error"] != nil || write["result"].(map[string]any)["status"] != "ok" {
				t.Fatalf("全局配置保存失败：%v", write)
			}
			var saved map[string]any
			if err := toml.Unmarshal(after, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["model"] != "relay-selected-model" || saved["model_reasoning_effort"] != "high" || saved["model_provider"] != nativeProviderID {
				t.Fatal("全局默认模型与推理档位未正确持久化")
			}
			if strings.HasPrefix(mode, "scoped-") {
				if _, err := a.BuildLaunchInputs(artifact, nil); err != nil {
					t.Fatal(err)
				}
				restarted, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := decodeTOML(t, restarted); got["model"] != "relay-selected-model" || got["model_reasoning_effort"] != "high" {
					t.Fatal("重新启动覆盖了保存的模型")
				}
				base, _ := os.ReadFile(filepath.Join(home, "config.toml"))
				if string(base) != original {
					t.Fatal("改动了全局配置")
				}
			}
			t.Log("配置可保存模型与推理档位，保留 custom 身份；再次启动保留偏好")
		})
	}
}
