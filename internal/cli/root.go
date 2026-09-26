package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/adapter/claudecode"
	"github.com/bingame/cli-relay/internal/adapter/codex"
	"github.com/bingame/cli-relay/internal/provider"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/bingame/cli-relay/internal/secrets"
	"github.com/bingame/cli-relay/internal/version"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type App struct {
	Home     string
	Adapters map[string]adapter.LaunchAdapter
}
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("原生进程退出码 %d", e.code) }
func IsExit(err error) bool       { var e exitError; return errors.As(err, &e) }
func ExitCode(err error) int {
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	var x *exec.ExitError
	if errors.As(err, &x) {
		if x.ExitCode() > 0 {
			return x.ExitCode()
		}
	}
	return 1
}
func NewRoot() *cobra.Command { return NewRootWithApp(&App{}) }
func NewRootWithApp(a *App) *cobra.Command {
	if a.Home == "" {
		a.Home = os.Getenv("RELAY_HOME")
		if a.Home == "" {
			h, _ := os.UserHomeDir()
			a.Home = filepath.Join(h, ".relay")
		}
	}
	if a.Adapters == nil {
		c := claudecode.New()
		x := codex.New()
		if mode := os.Getenv("RELAY_CODEX_LAUNCH_MODE"); mode != "" {
			x.LaunchMode = mode
		}
		a.Adapters = map[string]adapter.LaunchAdapter{"claude": c, "codex": x}
	}
	cmd := &cobra.Command{Use: "relay", Short: "为 AI Agent CLI 选择供应商并交接会话", SilenceUsage: true, SilenceErrors: true}
	cmd.Version = version.String()
	cmd.SetVersionTemplate("relay {{.Version}}\n")
	cmd.PersistentFlags().StringVar(&a.Home, "home", a.Home, "Relay 数据目录（默认 ~/.relay）")
	if x, ok := a.Adapters["codex"].(*codex.Adapter); ok {
		cmd.PersistentFlags().StringVar(&x.LaunchMode, "codex-launch-mode", x.LaunchMode, "Codex 配置方式：override 或 profile（profile 需先 switch）")
	}
	cmd.AddCommand(a.providerCommand(), a.secretCommand(), a.switchCommand(), a.launchCommand(adapter.Interactive), a.launchCommand(adapter.Headless), a.statusCommand(), a.handoffCommand(), a.skillCommand(), a.completionCommand())
	return cmd
}
func (a *App) adapter(target string) (adapter.LaunchAdapter, error) {
	v, ok := a.Adapters[target]
	if !ok {
		return nil, fmt.Errorf("暂不支持目标 CLI: %s", target)
	}
	return v, nil
}
func (a *App) lock(ctx context.Context) (func(), error) {
	if e := safeio.EnsureDir(a.Home); e != nil {
		return nil, e
	}
	p := filepath.Join(a.Home, ".lock")
	if e := safeio.CheckPath(p); e != nil {
		return nil, e
	}
	f := flock.New(p)
	ok, e := f.TryLockContext(ctx, 50*time.Millisecond)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, fmt.Errorf("无法获取数据目录锁")
	}
	return func() { _ = f.Unlock(); _ = f.Close() }, nil
}
func (a *App) vault(cmd *cobra.Command) (*secrets.Vault, error) {
	return secrets.Open(a.Home, secrets.Options{Passphrase: os.Getenv("RELAY_PASSPHRASE"), Prompt: func(confirm bool) (string, error) {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", fmt.Errorf("无系统密钥库；非交互模式请设置 RELAY_PASSPHRASE（至少 12 字符）")
		}
		fmt.Fprint(cmd.ErrOrStderr(), "本地加密口令：")
		b, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if e != nil {
			return "", e
		}
		if confirm {
			fmt.Fprint(cmd.ErrOrStderr(), "再次输入口令：")
			c, e := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.ErrOrStderr())
			if e != nil {
				return "", e
			}
			if string(b) != string(c) {
				return "", fmt.Errorf("口令不一致")
			}
		}
		return string(b), nil
	}})
}
func (a *App) store(cmd *cobra.Command, unlock bool) (*provider.Store, error) {
	var v provider.Cipher
	if unlock {
		key, e := a.vault(cmd)
		if e != nil {
			return nil, e
		}
		v = key
	}
	return provider.Open(a.Home, v)
}
func (a *App) current() (map[string]string, error) {
	v := map[string]string{}
	b, e := safeio.ReadRegular(filepath.Join(a.Home, "current.json"), 1<<20)
	if errors.Is(e, os.ErrNotExist) {
		return v, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return nil, fmt.Errorf("current.json 损坏")
	}
	return v, nil
}
func (a *App) saveCurrent(v map[string]string) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	return safeio.WriteFile(filepath.Join(a.Home, "current.json"), b, 0600)
}
func nativeHome(target string) string {
	h, _ := os.UserHomeDir()
	if target == "codex" {
		if v := os.Getenv("CODEX_HOME"); v != "" {
			return v
		}
		return filepath.Join(h, ".codex")
	}
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(h, ".claude")
}
func outputJSON(cmd *cobra.Command, v any) error {
	e := json.NewEncoder(cmd.OutOrStdout())
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	return e.Encode(v)
}
