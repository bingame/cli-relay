package main

import (
	"context"
	"fmt"
	"github.com/bingame/cli-relay/internal/cli"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	root := cli.NewRoot()
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if name == "relay-codex" || name == "relay-claude" {
		target := "codex"
		if name == "relay-claude" {
			target = "claude"
		}
		args := []string{"exec", target}
		if p := os.Getenv("RELAY_PROVIDER"); p != "" {
			args = append(args, "--provider", p)
		}
		args = append(args, "--")
		args = append(args, os.Args[1:]...)
		root.SetArgs(args)
	}
	if err := root.ExecuteContext(ctx); err != nil {
		code := cli.ExitCode(err)
		if !cli.IsExit(err) {
			fmt.Fprintln(os.Stderr, "relay:", err)
		}
		os.Exit(code)
	}
}
