package codex

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/handoff"
)

func TestReviewRPCHelper(t *testing.T) {
	switch os.Getenv("RELAY_REVIEW_RPC_HELPER") {
	case "descendant":
		time.Sleep(3 * time.Second)
		os.Exit(0)
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestReviewRPCHelper$")
		child.Env = append(os.Environ(), "RELAY_REVIEW_RPC_HELPER=descendant")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
}

func TestReviewThreadReadHonorsTimeoutWithInheritedPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	inputs := adapter.LaunchInputs{Binary: os.Args[0], Args: []string{"-test.run=^TestReviewRPCHelper$"}, Env: map[string]string{"RELAY_REVIEW_RPC_HELPER": "parent"}}
	started := time.Now()
	_, err := handoff.ReadCodexThread(ctx, inputs, t.TempDir(), "review-thread")
	if err == nil {
		t.Fatal("超时的 RPC 应返回错误")
	}
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatal("RPC 读取在上下文结束后仍被继承 stdout 的后代进程阻塞")
	}
}
