package handoff

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/process"
	"github.com/bingame/cli-relay/internal/provider"
)

// ReadCodexThread 优先通过官方 app-server 读取结构化历史，不启动模型回合。
func ReadCodexThread(ctx context.Context, inputs adapter.LaunchInputs, dir, id string) (string, error) {
	if !provider.ValidID(id) {
		return "", fmt.Errorf("无效会话 ID")
	}
	binary, e := process.ResolveBinary(inputs.Binary)
	if e != nil {
		return "", e
	}
	cmd := exec.CommandContext(ctx, binary, append(append([]string{}, inputs.Args...), "app-server", "--listen", "stdio://")...)
	cmd.Dir = dir
	cmd.Env = process.Environment(os.Environ(), inputs.UnsetEnv, inputs.Env)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	process.Configure(cmd)
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return "", e
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return "", e
	}
	if e = cmd.Start(); e != nil {
		return "", e
	}
	stopTree, e := process.Manage(cmd)
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", e
	}
	defer func() { stdin.Close(); stdout.Close(); stopTree(); _ = cmd.Wait() }()
	stopCancel := context.AfterFunc(ctx, func() { _ = stdin.Close(); _ = stdout.Close() })
	defer stopCancel()
	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	send := func(v any) error { return encoder.Encode(v) }
	read := func(id int) (map[string]any, error) {
		for scanner.Scan() {
			var frame map[string]any
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				continue
			}
			if got, ok := frame["id"].(float64); ok && got == float64(id) {
				if frame["error"] != nil {
					return nil, fmt.Errorf("Codex thread/read 接口拒绝请求")
				}
				result, ok := frame["result"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("Codex RPC 返回格式无效")
				}
				return result, nil
			}
		}
		if e := scanner.Err(); e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("Codex app-server 提前结束")
	}
	if e = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "relay", "title": "Relay", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true}}}); e != nil {
		return "", e
	}
	if _, e = read(1); e != nil {
		return "", e
	}
	if e = send(map[string]any{"method": "initialized", "params": map[string]any{}}); e != nil {
		return "", e
	}
	if e = send(map[string]any{"id": 2, "method": "thread/read", "params": map[string]any{"threadId": id, "includeTurns": true}}); e != nil {
		return "", e
	}
	result, e := read(2)
	if e != nil {
		return "", e
	}
	var text strings.Builder
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			typ, _ := v["type"].(string)
			switch typ {
			case "userMessage", "user_message":
				if content, ok := v["content"].([]any); ok {
					for _, part := range content {
						if p, ok := part.(map[string]any); ok && p["type"] == "text" {
							if s, ok := p["text"].(string); ok {
								text.WriteString(EncodeRecord("user", s))
							}
						}
					}
				}
				return
			case "agentMessage", "agent_message":
				if s, ok := v["text"].(string); ok {
					text.WriteString(EncodeRecord("assistant", s))
				}
				return
			case "commandExecution", "fileChange", "mcpToolCall":
				text.WriteString(EncodeRecord("tool", stringifyClean(v)))
				return
			case "reasoning":
				return
			}
			for _, key := range []string{"thread", "turns", "items"} {
				if child, ok := v[key]; ok {
					walk(child)
				}
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(result)
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("结构化会话不含可交接文本")
	}
	return strings.TrimSpace(text.String()), nil
}
