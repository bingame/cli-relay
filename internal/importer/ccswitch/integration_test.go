package ccswitch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/adapter/claudecode"
	"github.com/bingame/cli-relay/internal/adapter/codex"
)

// 只有显式提供路径才接触真实 dump；所有产物写入测试隔离目录，不调用真实 CLI。
func TestRealDumpAdapterIntegration(t *testing.T) {
	path := os.Getenv("RELAY_TEST_CCSWITCH_DUMP")
	if path == "" {
		t.Skip("显式指定真实 dump 路径后才运行；只输出计数")
	}
	result, err := Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// 真实 dump 是用户数据，供应商数量会随上游增删变化；期望值从 dump 现场推导。
	expected := 0
	for _, entry := range result.Providers {
		if target := entry.Provider.Target; target == "claude" || target == "codex" {
			expected++
		}
	}
	if expected == 0 {
		t.Skip("该导出中没有受支持的 claude/codex 供应商")
	}
	checked := checkAdapterIntegration(t, result)
	if checked != expected {
		t.Fatalf("期望验证 %d 条受支持供应商，实际 %d", expected, checked)
	}
	t.Logf("已验证 %d 条供应商及其冲突改名版本：产物、argv 无明文已提取凭据", checked)
}

func TestFixtureAdapterIntegration(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "ccswitch", "sample.sql"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Parse(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if checked := checkAdapterIntegration(t, result); checked != 2 {
		t.Fatalf("期望验证 2 条受支持供应商，实际 %d", checked)
	}
}

func TestClaudeAPIKeyOnlyAdapterIntegration(t *testing.T) {
	row := validRow()
	row["settings_config"] = `{"env":{"ANTHROPIC_API_KEY":"fake-api-key-only"}}`
	entry, err := parseEntry(row)
	if err != nil {
		t.Fatal(err)
	}
	checkAdapterIntegration(t, &Result{Providers: []Entry{entry}})
}

func checkAdapterIntegration(t *testing.T, result *Result) int {
	t.Helper()
	// Codex 的 BuildLaunchInputs 会安装 profile，NativeHome 必须隔离到临时目录，
	// 否则集成测试会写进用户真实的 $CODEX_HOME（config.toml + <id>.config.toml）。
	codexAdapter := codex.New()
	codexAdapter.NativeHome = t.TempDir()
	adapters := map[string]adapter.LaunchAdapter{"claude": claudecode.New(), "codex": codexAdapter}
	root := t.TempDir()
	checked := 0
	for index, entry := range result.Providers {
		for _, target := range []string{entry.Provider.Target} {
			ad, supported := adapters[target]
			if !supported {
				if entry.Secrets["source_settings"] == "" {
					t.Fatalf("第 %d 条未知目标缺少加密用源配置", index+1)
				}
				continue
			}
			for _, renamed := range []bool{false, true} {
				p := entry.Provider
				if renamed {
					p.ID = "renamed-imported-1"
				}
				// SQLite 存储 extra_json 会经过 JSON 往返，整数会变成 float64。
				stored, err := json.Marshal(p)
				if err != nil || json.Unmarshal(stored, &p) != nil {
					t.Fatal("无法模拟供应商数据库 JSON 往返")
				}
				artifact, err := ad.Render(p, root, entry.Models...)
				if err != nil {
					t.Errorf("第 %d 条 %s 配置渲染失败（改名=%t）：%v", index+1, target, renamed, err)
					continue
				}
				inputs, err := ad.BuildLaunchInputs(artifact, adapter.ResolvedSecrets(entry.Secrets))
				if err != nil {
					t.Errorf("第 %d 条 %s 启动输入生成失败（改名=%t）：%v", index+1, target, renamed, err)
					continue
				}
				actual, err := os.ReadFile(artifact.Path)
				if err != nil {
					t.Fatal("无法读取隔离目录中的适配器产物")
				}
				encoded, err := json.Marshal(p)
				if err != nil {
					t.Fatal("无法编码供应商元数据")
				}
				for key, value := range entry.Secrets {
					if key == "source_settings" || key == "source_meta" || key == "api_key_env" || len(value) < 8 {
						continue
					}
					for _, output := range []string{string(encoded), string(artifact.Content), string(actual), strings.Join(inputs.Args, "\n")} {
						if strings.Contains(output, value) {
							t.Errorf("第 %d 条 %s 配置存在凭据输出泄漏（改名=%t）", index+1, target, renamed)
						}
					}
				}
				if value := entry.Secrets["api_key"]; value != "" && strings.Contains(strings.Join(inputs.Args, "\n"), value) {
					t.Errorf("第 %d 条 %s 主凭据进入 argv（改名=%t）", index+1, target, renamed)
				}
			}
			checked++
		}
	}
	return checked
}
