package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/adapter/mock"
	"github.com/bingame/cli-relay/internal/handoff"
	"github.com/bingame/cli-relay/internal/process"
)

func sampleDoc() []byte {
	d := &handoff.Document{SchemaVersion: 1, SourceCLI: "claude", SourceProvider: "source", SourceSessionID: "session", GeneratedBy: "live-agent", GeneratedAt: time.Now().UTC().Format(time.RFC3339), Sections: map[string]string{}}
	for _, s := range handoff.SectionTitles {
		d.Sections[s] = "已确认的会话内容"
	}
	d.Sections[handoff.ConstraintsSection] = "文档和Commit使用中文"
	b, _ := d.Marshal()
	return b
}
func TestCLIHandoffHelper(t *testing.T) {
	if os.Getenv("RELAY_CLI_HELPER") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "--resume" || arg == "resume" {
			os.Exit(12)
		}
	}
	b, _ := io.ReadAll(os.Stdin)
	if p := os.Getenv("RELAY_CAPTURE_PROMPT"); p != "" {
		_ = os.WriteFile(p, b, 0600)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": string(sampleDoc())})
	os.Exit(0)
}
func TestHandoffLiveValidateContinueAndRawFallback(t *testing.T) {
	t.Setenv("RELAY_PASSPHRASE", "test-passphrase-long-enough")
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	ad := &mock.Adapter{Name: "claude", Resume: []string{"--resume"}, Inputs: adapter.LaunchInputs{Binary: os.Args[0], Args: []string{"-test.run=^TestCLIHandoffHelper$", "--"}, Env: map[string]string{"RELAY_CLI_HELPER": "1", "RELAY_CAPTURE_PROMPT": capture}}}
	app := &App{Home: home, Adapters: map[string]adapter.LaunchAdapter{"claude": ad}}
	run := func(input string, args ...string) (string, error) {
		cmd := NewRootWithApp(app)
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(input))
		out := new(bytes.Buffer)
		cmd.SetOut(out)
		cmd.SetErr(io.Discard)
		e := cmd.Execute()
		return out.String(), e
	}
	for _, id := range []string{"source", "summary"} {
		if _, e := run("unusual-opaque-source-key", "provider", "add", "--id", id, "--target", "claude", "--api-key-stdin"); e != nil {
			t.Fatal(e)
		}
	}
	docPath := filepath.Join(t.TempDir(), "handoff.md")
	if _, e := run(string(sampleDoc()), "handoff", "export", "--cli", "claude", "--input", "-", "-o", docPath); e != nil {
		t.Fatal(e)
	}
	if _, e := run("", "handoff", "schema", "--validate", docPath); e != nil {
		t.Fatal(e)
	}
	if _, e := run("", "handoff", "continue", "--doc", docPath, "--provider", "summary", "--exec"); e != nil {
		t.Fatal(e)
	}
	prompt, _ := os.ReadFile(capture)
	if strings.Count(string(prompt), "文档和Commit使用中文") < 2 {
		t.Fatal("硬约束未单独强调")
	}
	rawPath := filepath.Join(t.TempDir(), "session.jsonl")
	_ = os.WriteFile(rawPath, []byte(`{"type":"user","message":{"role":"user","content":"继续实现；unusual-opaque-source-key"}}`+"\n{truncated"), 0600)
	dir, _ := os.Getwd()
	if e := process.SaveSession(home, process.Session{CLI: "claude", Provider: "source", SessionID: "session", WorkDir: dir}); e != nil {
		t.Fatal(e)
	}
	out, e := run("", "handoff", "export", "--cli", "claude", "--session", "session", "--dead", "--raw-file", rawPath, "--summary-provider", "summary")
	if e != nil {
		t.Fatal(e)
	}
	d, e := handoff.Parse([]byte(out))
	if e != nil || d.GeneratedBy != "raw-file-fallback" {
		t.Fatal(e, out)
	}
	prompt, _ = os.ReadFile(capture)
	if bytes.Contains(prompt, []byte("unusual-opaque-source-key")) {
		t.Fatal("原会话凭据泄漏到总结模型")
	}
	if !strings.Contains(d.Sections["环境依赖声明"], "降级") {
		t.Fatal("未标记降级")
	}
}
