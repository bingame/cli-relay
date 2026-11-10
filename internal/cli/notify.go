package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bingame/cli-relay/internal/adapter/codex"
	"github.com/bingame/cli-relay/internal/notifyhook"
	"github.com/spf13/cobra"
)

func (a *App) notifyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Hook 远程通知：任务完成/需要人工介入时推送", SilenceUsage: true}
	cmd.AddCommand(a.notifySetCommand(), a.notifyClearCommand(), a.notifyInstallCommand(), a.notifyUninstallCommand(), a.notifyStatusCommand(), a.notifyTestCommand(), a.notifyHookCommand())
	return cmd
}

func (a *App) notifySetCommand() *cobra.Command {
	var (
		channelType string
		endpoint    string
		topic       string
		chatID      string
		tokenStdin  bool
	)
	cmd := &cobra.Command{Use: "set", Short: "配置通知通道", RunE: func(cmd *cobra.Command, args []string) error {
		var token string
		if tokenStdin {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8193))
			if err != nil {
				return err
			}
			if len(data) > 8192 {
				return fmt.Errorf("令牌超出长度限制")
			}
			token = strings.TrimSpace(string(data))
		}
		channel := notifyhook.Channel{Type: channelType, Endpoint: endpoint, Token: token, ChatID: chatID, Topic: topic}
		unlock, err := a.lock(cmd.Context())
		if err != nil {
			return err
		}
		defer unlock()
		vault, err := a.vault(cmd)
		if err != nil {
			return err
		}
		if err := notifyhook.SaveChannel(a.Home, vault, channel); err != nil {
			return err
		}
		return outputJSON(cmd, map[string]any{
			"type":             channel.Type,
			"endpoint":         channel.Endpoint,
			"topic":            channel.Topic,
			"chat_id":          channel.ChatID,
			"token_configured": channel.Token != "",
		})
	}}
	cmd.Flags().StringVar(&channelType, "type", "", "通道类型：webhook|bark|telegram|ntfy")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "通道端点（webhook URL；bark/ntfy/telegram 服务器地址，空用默认）")
	cmd.Flags().StringVar(&topic, "topic", "", "ntfy topic")
	cmd.Flags().StringVar(&chatID, "chat-id", "", "Telegram chat id")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "从标准输入读取令牌（bark key / bot token / Bearer 等）")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func (a *App) notifyClearCommand() *cobra.Command {
	return &cobra.Command{Use: "clear", Short: "清除通知通道", RunE: func(cmd *cobra.Command, args []string) error {
		unlock, err := a.lock(cmd.Context())
		if err != nil {
			return err
		}
		defer unlock()
		cleared, err := notifyhook.ClearChannel(a.Home)
		if err != nil {
			return err
		}
		return outputJSON(cmd, map[string]any{"cleared": cleared})
	}}
}

func notifyTargets(claude, codexFlag, zcode bool) (targets []string) {
	if !claude && !codexFlag && !zcode {
		return []string{"claude", "codex", "zcode"}
	}
	if claude {
		targets = append(targets, "claude")
	}
	if codexFlag {
		targets = append(targets, "codex")
	}
	if zcode {
		targets = append(targets, "zcode")
	}
	return targets
}

func (a *App) notifyInstallCommand() *cobra.Command {
	var withClaude, withCodex, withZcode bool
	cmd := &cobra.Command{Use: "install", Short: "向各 CLI 用户级配置写入通知 hook 条目", RunE: func(cmd *cobra.Command, args []string) error {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		if binary, err = filepath.Abs(binary); err != nil {
			return err
		}
		unlock, err := a.lock(cmd.Context())
		if err != nil {
			return err
		}
		defer unlock()
		state, err := notifyhook.LoadState(a.Home)
		if err != nil {
			return err
		}
		result := map[string]any{}
		var failures []error
		for _, target := range notifyTargets(withClaude, withCodex, withZcode) {
			switch target {
			case "claude":
				command, err := notifyhook.ClaudeTarget{NativeHome: nativeHome("claude")}.Install(binary)
				if err != nil {
					failures = append(failures, fmt.Errorf("claude: %w", err))
					continue
				}
				state.Claude.Installed = true
				state.Claude.Command = command
				result["claude"] = map[string]any{"events": []string{"Stop", "Notification", "PreToolUse"}}
			case "codex":
				command := []string{binary, "notify", "hook", "codex"}
				previous, replacedOther, err := codex.NotifyInstall(nativeHome("codex"), command)
				if err != nil {
					failures = append(failures, fmt.Errorf("codex: %w", err))
					continue
				}
				if replacedOther {
					state.Codex.Previous = previous
					state.Codex.HasPrevious = true
				}
				state.Codex.Installed = true
				result["codex"] = map[string]any{"chained_previous_notify": replacedOther}
			case "zcode":
				path, err := notifyhook.ZcodeConfigPath()
				if err != nil {
					failures = append(failures, fmt.Errorf("zcode: %w", err))
					continue
				}
				previous, err := notifyhook.ZcodeTarget{ConfigPath: path}.Install(binary)
				if err != nil {
					failures = append(failures, fmt.Errorf("zcode: %w", err))
					continue
				}
				state.ZCode.Installed = true
				state.ZCode.PreviousEnabled = previous
				result["zcode"] = map[string]any{"events": []string{"Stop", "PermissionRequest", "PreToolUse"}, "hooks_enabled": true}
			}
		}
		state.Binary = binary
		if err := notifyhook.SaveState(a.Home, state); err != nil {
			return err
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		return outputJSON(cmd, result)
	}}
	cmd.Flags().BoolVar(&withClaude, "claude", false, "安装到 Claude Code (~/.claude/settings.json)")
	cmd.Flags().BoolVar(&withCodex, "codex", false, "安装到 Codex (~/.codex/config.toml notify)")
	cmd.Flags().BoolVar(&withZcode, "zcode", false, "安装到 ZCode (~/.zcode/cli/config.json)")
	return cmd
}

func (a *App) notifyUninstallCommand() *cobra.Command {
	var withClaude, withCodex, withZcode bool
	cmd := &cobra.Command{Use: "uninstall", Short: "从各 CLI 配置中移除 relay 的通知条目", RunE: func(cmd *cobra.Command, args []string) error {
		unlock, err := a.lock(cmd.Context())
		if err != nil {
			return err
		}
		defer unlock()
		state, err := notifyhook.LoadState(a.Home)
		if err != nil {
			return err
		}
		result := map[string]any{}
		var failures []error
		for _, target := range notifyTargets(withClaude, withCodex, withZcode) {
			switch target {
			case "claude":
				removed, err := notifyhook.ClaudeTarget{NativeHome: nativeHome("claude")}.Uninstall()
				if err != nil {
					failures = append(failures, fmt.Errorf("claude: %w", err))
					continue
				}
				state.Claude.Installed = false
				state.Claude.Command = ""
				result["claude"] = map[string]any{"removed_events": removed}
			case "codex":
				var previous []string
				if state.Codex.HasPrevious {
					previous = state.Codex.Previous
				}
				restored, err := codex.NotifyRestore(nativeHome("codex"), previous)
				if err != nil {
					failures = append(failures, fmt.Errorf("codex: %w", err))
					continue
				}
				state.Codex.Installed = false
				state.Codex.Previous = nil
				state.Codex.HasPrevious = false
				result["codex"] = map[string]any{"notify_restored": restored}
			case "zcode":
				path, err := notifyhook.ZcodeConfigPath()
				if err != nil {
					failures = append(failures, fmt.Errorf("zcode: %w", err))
					continue
				}
				removed, err := notifyhook.ZcodeTarget{ConfigPath: path}.Uninstall(state.ZCode.PreviousEnabled)
				if err != nil {
					failures = append(failures, fmt.Errorf("zcode: %w", err))
					continue
				}
				state.ZCode.Installed = false
				state.ZCode.PreviousEnabled = nil
				result["zcode"] = map[string]any{"removed_events": removed}
			}
		}
		if err := notifyhook.SaveState(a.Home, state); err != nil {
			return err
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		return outputJSON(cmd, result)
	}}
	cmd.Flags().BoolVar(&withClaude, "claude", false, "从 Claude Code 移除")
	cmd.Flags().BoolVar(&withCodex, "codex", false, "从 Codex 移除并还原原 notify")
	cmd.Flags().BoolVar(&withZcode, "zcode", false, "从 ZCode 移除")
	return cmd
}

func (a *App) notifyStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "查看通道配置与各 CLI 安装状态", RunE: func(cmd *cobra.Command, args []string) error {
		state, err := notifyhook.LoadState(a.Home)
		if err != nil {
			return err
		}
		channel := map[string]any{"configured": notifyhook.ChannelExists(a.Home)}
		if channel["configured"] == true {
			if vault, err := a.vault(cmd); err == nil {
				if ch, ok, _ := notifyhook.LoadChannel(a.Home, vault); ok {
					channel["type"] = ch.Type
					channel["endpoint"] = ch.Endpoint
					channel["topic"] = ch.Topic
					channel["chat_id"] = ch.ChatID
				}
			} else {
				channel["locked"] = true
			}
		}
		claudeEvents, err := notifyhook.ClaudeTarget{NativeHome: nativeHome("claude")}.Status()
		if err != nil {
			return err
		}
		codexCurrent, codexOurs, err := codex.NotifyInspect(nativeHome("codex"))
		if err != nil {
			return err
		}
		zcodePath, err := notifyhook.ZcodeConfigPath()
		if err != nil {
			return err
		}
		zcodeEvents, err := notifyhook.ZcodeTarget{ConfigPath: zcodePath}.Status()
		if err != nil {
			return err
		}
		return outputJSON(cmd, map[string]any{
			"channel": channel,
			"claude":  map[string]any{"installed": state.Claude.Installed, "events": claudeEvents},
			"codex":   map[string]any{"installed": state.Codex.Installed, "notify": codexCurrent, "ours": codexOurs, "chained": state.Codex.HasPrevious},
			"zcode":   map[string]any{"installed": state.ZCode.Installed, "events": zcodeEvents},
			"log":     notifyhook.LogTail(a.Home, 2048),
		})
	}}
}

func (a *App) notifyTestCommand() *cobra.Command {
	return &cobra.Command{Use: "test", Short: "发送测试通知", RunE: func(cmd *cobra.Command, args []string) error {
		vault, err := a.vault(cmd)
		if err != nil {
			return err
		}
		channel, ok, err := notifyhook.LoadChannel(a.Home, vault)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("尚未配置通知通道，请先 relay notify set")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		event := notifyhook.Event{Kind: notifyhook.KindTaskCompleted, CLI: "test", Project: "relay-notify", Message: "relay notify 测试消息"}
		if err := notifyhook.Send(ctx, a.Home, vault, event); err != nil {
			return err
		}
		return outputJSON(cmd, map[string]any{"sent": true, "type": channel.Type})
	}}
}

// notifyHookCommand 是写入各 CLI 配置的内部入口：读取 hook 载荷并推送。
// 它绝不写 stdout、绝不返回非零退出码，失败只记录到 notify.log，
// 保证不影响宿主 CLI 会话。
func (a *App) notifyHookCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook <claude|codex|zcode> [payload]",
		Short:  "内部命令：由 CLI hook 调用",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if target != "claude" && target != "codex" && target != "zcode" {
				return nil
			}
			logFail := func(format string, values ...any) {
				notifyhook.LogError(a.Home, format, values...)
			}
			var event notifyhook.Event
			var codexPayload string
			if target == "codex" {
				if len(args) < 2 {
					return nil
				}
				codexPayload = args[len(args)-1]
				parsed, err := notifyhook.ParseCodexEvent(codexPayload)
				if err != nil {
					logFail("codex hook 载荷解析失败: %v", err)
					return nil
				}
				event = parsed
			} else {
				data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
				if err != nil {
					logFail("%s hook stdin 读取失败: %v", target, err)
					return nil
				}
				parsed, err := notifyhook.ParseStdinEvent(target, data)
				if err != nil {
					logFail("%s hook 载荷解析失败: %v", target, err)
					return nil
				}
				event = parsed
			}
			// Codex 链式转发与 relay 自身的通知相互独立：即使未配置通道、
			// 或本次事件被去重，也要让被接管的原 notify 程序照常收到载荷。
			var state notifyhook.State
			if target == "codex" {
				if st, err := notifyhook.LoadState(a.Home); err == nil {
					state = st
				}
			}
			if target == "codex" && state.Codex.Installed && state.Codex.HasPrevious {
				notifyhook.ForwardPrevious(a.Home, state.Codex.Previous, codexPayload)
			}
			if event.Skip {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			unlock, err := a.lock(ctx)
			if err != nil {
				logFail("获取数据目录锁失败: %v", err)
				return nil
			}
			allowed := notifyhook.AllowOnce(a.Home, target+"|"+event.Session+"|"+event.Kind, time.Now())
			unlock()
			if !allowed {
				return nil
			}
			vault, err := a.vault(cmd)
			if err != nil {
				logFail("密钥库不可用（passphrase 后端需 RELAY_PASSPHRASE）: %v", err)
				return nil
			}
			if err := notifyhook.Send(ctx, a.Home, vault, event); err != nil && !notifyhook.IsNoChannel(err) {
				logFail("通知发送失败 kind=%s cli=%s: %v", event.Kind, target, err)
			}
			return nil
		},
	}
	return cmd
}
