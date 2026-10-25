package notifyhook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/bingame/cli-relay/internal/plugins"
	"github.com/bingame/cli-relay/internal/secrets"
)

// BuildNotifier 按通道类型构造推送适配器。
func BuildNotifier(ch Channel) (plugins.Notifier, error) {
	switch ch.Type {
	case "webhook":
		return plugins.NewWebhook(ch.Endpoint, ch.Token)
	case "bark":
		return plugins.NewBark(ch.Endpoint, ch.Token)
	case "telegram":
		return plugins.NewTelegram(ch.Endpoint, ch.Token, ch.ChatID)
	case "ntfy":
		return plugins.NewNtfy(ch.Endpoint, ch.Topic, ch.Token)
	default:
		return nil, fmt.Errorf("未知通知通道类型: %s", ch.Type)
	}
}

var errNoChannel = fmt.Errorf("尚未配置通知通道（relay notify set）")

// IsNoChannel 报告错误是否为“未配置通道”，hook 路径据此静默跳过。
func IsNoChannel(err error) bool { return err == errNoChannel }

// Send 读取通道并发送一次事件通知。
func Send(ctx context.Context, relayHome string, v *secrets.Vault, ev Event) error {
	ch, ok, err := LoadChannel(relayHome, v)
	if err != nil {
		return err
	}
	if !ok {
		return errNoChannel
	}
	notifier, err := BuildNotifier(ch)
	if err != nil {
		return err
	}
	return notifier.Notify(ctx, plugins.NotificationEvent{Type: ev.Kind, Payload: PayloadFor(ev)})
}

// PayloadFor 构造适配器约定的事件载荷键：title、body、cli、host、project、session。
func PayloadFor(ev Event) map[string]any {
	title := "需要人工介入"
	if ev.Kind == KindTaskCompleted {
		title = "任务完成"
	}
	host, _ := os.Hostname()
	body := fmt.Sprintf("%s · %s · %s", host, ev.Project, ev.CLI)
	if ev.Session != "" {
		body += " · " + shortSession(ev.Session)
	}
	if ev.Message != "" {
		body += "\n" + ev.Message
	}
	return map[string]any{
		"title":   title,
		"body":    body,
		"cli":     ev.CLI,
		"host":    host,
		"project": ev.Project,
		"session": shortSession(ev.Session),
	}
}

func shortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ForwardPrevious 把 Codex notify 载荷原样转发给被链式接管的原程序；
// best-effort，失败只记日志，不影响通知结果。
func ForwardPrevious(relayHome string, program []string, payload string) {
	if len(program) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := append([]string{}, program[1:]...)
	args = append(args, payload)
	cmd := exec.CommandContext(ctx, program[0], args...)
	if err := cmd.Run(); err != nil {
		LogError(relayHome, "Codex notify 链式转发失败: %v", err)
	}
}
