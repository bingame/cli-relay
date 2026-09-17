package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("RELAY_PROCESS_TEST") != "1" {
		return
	}
	switch os.Getenv("RELAY_PROCESS_CASE") {
	case "error":
		fmt.Fprintln(os.Stdout, `{"type":"thread.started","thread_id":"test-session"}`)
		fmt.Fprintln(os.Stdout, `{"type":"turn.failed","error":{"message":"stream disconnected dummy-test-key"}}`)
		os.Exit(1)
	case "human":
		fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"AskUserQuestion"}]}}`)
		os.Exit(0)
	case "recovered":
		fmt.Fprintln(os.Stdout, `{"type":"error","message":"stream disconnected, reconnecting"}`)
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed"}`)
		os.Exit(0)
	case "large":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", 3<<20))
		fmt.Fprintln(os.Stdout, `{"type":"result","result":"done"}`)
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(0)
}
func helperOptions(t *testing.T, which string) (Options, *bytes.Buffer) {
	t.Helper()
	buf := new(bytes.Buffer)
	return Options{Inputs: adapter.LaunchInputs{Binary: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"}, Env: map[string]string{"RELAY_PROCESS_TEST": "1", "RELAY_PROCESS_CASE": which}}, Stdout: buf, Stderr: new(bytes.Buffer), Root: t.TempDir(), Target: "codex", Provider: "test", SecretValues: []string{"dummy-test-key"}}, buf
}
func TestSupervisedExitSessionAndRedaction(t *testing.T) {
	o, b := helperOptions(t, "error")
	r, e := Spawn(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if r.ExitCode != 10 || r.NativeExitCode != 1 || r.SessionID != "test-session" {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(b.String(), "dummy-test-key") {
		t.Fatal("密钥出现在stdout")
	}
	s, e := LoadSession(o.Root, "codex", "test-session")
	if e != nil || s.Provider != "test" {
		t.Fatal(e)
	}
	instances, e := Instances(o.Root)
	if e != nil || len(instances) != 0 {
		t.Fatal("残留实例", e)
	}
}
func TestHumanAndHugeLine(t *testing.T) {
	for _, c := range []struct {
		mode string
		code int
	}{{"human", 11}, {"large", 0}, {"recovered", 0}} {
		o, _ := helperOptions(t, c.mode)
		r, e := Spawn(context.Background(), o)
		if e != nil || r.ExitCode != c.code {
			t.Fatalf("%s: %+v %v", c.mode, r, e)
		}
	}
}
func TestCancellation(t *testing.T) {
	o, _ := helperOptions(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = Spawn(ctx, o)
	if time.Since(start) > 8*time.Second {
		t.Fatal("取消未能结束子进程")
	}
}
func TestStreamingRedactsAcrossBoundaries(t *testing.T) {
	b := new(bytes.Buffer)
	s := newStream(b, []string{"secret-12345"}, nil)
	for _, part := range []string{"before sec", "ret-12", "345 after"} {
		_, _ = s.Write([]byte(part))
	}
	_ = s.Close()
	if b.String() != "before [已隐藏凭据] after" {
		t.Fatal(b.String())
	}
}
func TestEnvironmentRemovesStaleAndPassphrase(t *testing.T) {
	got := Environment([]string{"STALE=old", "KEEP=yes", "RELAY_PASSPHRASE=dummy"}, []string{"STALE"}, map[string]string{"NEW": "ok"})
	text := strings.Join(got, "\n")
	if strings.Contains(text, "STALE") || strings.Contains(text, "PASSPHRASE") || !strings.Contains(text, "NEW=ok") {
		t.Fatal(got)
	}
}
