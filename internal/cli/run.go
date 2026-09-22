package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/process"
	"github.com/spf13/cobra"
)

func (a *App) prepare(cmd *cobra.Command, target, id string, mode adapter.Mode, native []string) (process.Options, error) {
	ad, e := a.adapter(target)
	if e != nil {
		return process.Options{}, e
	}
	if id == "" {
		current, e := a.current()
		if e != nil {
			return process.Options{}, e
		}
		id = current[target]
	}
	inputs := adapter.LaunchInputs{Binary: "claude"}
	if target == "codex" {
		inputs.Binary = "codex"
	}
	values := []string{}
	if id != "" {
		unlock, e := a.lock(cmd.Context())
		if e != nil {
			return process.Options{}, e
		}
		defer unlock()
		s, e := a.store(cmd, false)
		if e != nil {
			return process.Options{}, e
		}
		defer s.Close()
		p, e := a.resolveProvider(cmd.Context(), s, target, id)
		if e != nil {
			return process.Options{}, e
		}
		id = p.ID
		if !p.Supports(target) {
			return process.Options{}, fmt.Errorf("供应商不支持目标 CLI")
		}
		if p.EffectiveStatus() == "disabled" {
			return process.Options{}, fmt.Errorf("供应商已被 cc-switch 同步标记为失效: %s", p.ID)
		}
		models, e := s.Models(cmd.Context(), p.Target, p.ID)
		if e != nil {
			return process.Options{}, e
		}
		artifact, e := ad.Render(p, a.Home, models...)
		if e != nil {
			return process.Options{}, e
		}
		if _, e = os.Stat(filepath.Join(a.Home, "vault.json")); e == nil {
			v, e := a.vault(cmd)
			if e != nil {
				return process.Options{}, e
			}
			s.SetCipher(v)
		}
		sec, e := s.Secrets(cmd.Context(), p.Target, id)
		if e != nil {
			return process.Options{}, e
		}
		inputs, e = ad.BuildLaunchInputs(artifact, adapter.ResolvedSecrets(sec))
		if e != nil {
			return process.Options{}, e
		}
		for k, v := range sec {
			if k == "api_key" || strings.HasPrefix(k, "env:") {
				if v != "" {
					values = append(values, v)
				}
			}
		}
	}
	args, e := ad.NativeArgs(mode, native)
	if e != nil {
		return process.Options{}, e
	}
	inputs.Args = append(inputs.Args, args...)
	bridge := false
	if protocol, ok := ad.(interface{ IsProtocolBridge([]string) bool }); ok {
		bridge = protocol.IsProtocolBridge(args)
	}
	if binary := os.Getenv("RELAY_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_BIN"); binary != "" {
		inputs.Binary = binary
	}
	dir, e := os.Getwd()
	if e != nil {
		return process.Options{}, e
	}
	sessionDir := dir
	if target == "codex" {
		for i := 0; i < len(native); i++ {
			arg := native[i]
			if arg == "--" {
				break
			}
			value := ""
			if arg == "-C" || arg == "--cd" {
				if i+1 < len(native) {
					i++
					value = native[i]
				}
			} else if strings.HasPrefix(arg, "--cd=") {
				value = strings.TrimPrefix(arg, "--cd=")
			}
			if value != "" {
				sessionDir, e = filepath.Abs(value)
				if e != nil {
					return process.Options{}, e
				}
			}
		}
	}
	return process.Options{Inputs: inputs, Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(), Dir: dir, SessionDir: sessionDir, Root: a.Home, Target: target, Provider: id, SecretValues: values, ProtocolBridge: bridge}, nil
}
func (a *App) launchCommand(mode adapter.Mode) *cobra.Command {
	var id string
	cmd := &cobra.Command{Use: string(mode) + " <cli> [--provider <id>] [-- 原生参数...]", Short: map[adapter.Mode]string{adapter.Interactive: "交互启动（Unix 使用 execve）", adapter.Headless: "执行一次，转发输出并返回分类退出码"}[mode], Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if at := cmd.ArgsLenAtDash(); at < 0 && len(args) > 1 {
			return fmt.Errorf("原生参数请放在 -- 后面")
		}
		o, e := a.prepare(cmd, args[0], id, mode, args[1:])
		if e != nil {
			return e
		}
		if mode == adapter.Interactive {
			return process.Replace(o)
		}
		if process.IsTTY() {
			fmt.Fprintln(cmd.ErrOrStderr(), "提示：当前为交互终端；需要原生交互界面时请使用 relay run。")
		}
		result, e := process.Spawn(cmd.Context(), o)
		if e != nil {
			return e
		}
		if result.ExitCode != 0 {
			return exitError{result.ExitCode}
		}
		return nil
	}}
	cmd.ValidArgsFunction = a.completeTargetArg()
	cmd.Flags().StringVar(&id, "provider", "", "本次使用的供应商 ID 或显示名称（不修改默认）")
	_ = cmd.RegisterFlagCompletionFunc("provider", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		return a.completeProviders(target)(cmd, args, toComplete)
	})
	return cmd
}

func (a *App) providerSecretValues(cmd *cobra.Command, target, id string) ([]string, error) {
	unlock, e := a.lock(cmd.Context())
	if e != nil {
		return nil, e
	}
	defer unlock()
	s, e := a.store(cmd, false)
	if e != nil {
		return nil, e
	}
	defer s.Close()
	if _, e = os.Stat(filepath.Join(a.Home, "vault.json")); e == nil {
		v, e := a.vault(cmd)
		if e != nil {
			return nil, e
		}
		s.SetCipher(v)
	}
	p, e := a.resolveProvider(cmd.Context(), s, target, id)
	if e != nil {
		return nil, e
	}
	values, e := s.Secrets(cmd.Context(), p.Target, p.ID)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for k, v := range values {
		if v != "" && (k == "api_key" || strings.HasPrefix(k, "env:")) {
			out = append(out, v)
		}
	}
	return out, nil
}
