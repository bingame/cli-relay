//go:build !windows

package process

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"relay/internal/adapter"
)

func TestReplacePreservesPIDAndExitCode(t *testing.T) {
	if os.Getenv("RELAY_REPLACE_TEST") == "1" {
		e := Replace(Options{Inputs: adapter.LaunchInputs{Binary: "sh", Args: []string{"-c", "printf '%s' \"$$\"; exit 7"}}, Target: "test"})
		if e != nil {
			os.Exit(90)
		}
		os.Exit(91)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReplacePreservesPIDAndExitCode$")
	cmd.Env = append(os.Environ(), "RELAY_REPLACE_TEST=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	pid := cmd.Process.Pid
	e := cmd.Wait()
	if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatalf("未保留退出码: %v", e)
	}
	if strings.TrimSpace(out.String()) != strconv.Itoa(pid) {
		t.Fatalf("execve 改变了PID: parent=%d output=%q", pid, out.String())
	}
}
