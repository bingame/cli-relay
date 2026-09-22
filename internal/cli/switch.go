package cli

import (
	"fmt"
	"github.com/spf13/cobra"
)

func (a *App) switchCommand() *cobra.Command {
	var target string
	cmd := &cobra.Command{Use: "switch <provider>", Short: "合并原生配置并更新各 CLI 默认供应商", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		unlock, e := a.lock(cmd.Context())
		if e != nil {
			return e
		}
		defer unlock()
		s, e := a.store(cmd, false)
		if e != nil {
			return e
		}
		defer s.Close()
		p, e := a.resolveProvider(cmd.Context(), s, args[0])
		if e != nil {
			return e
		}
		if p.EffectiveStatus() == "disabled" {
			return fmt.Errorf("供应商已被 cc-switch 同步标记为失效: %s", p.ID)
		}
		models, e := s.Models(cmd.Context(), p.ID)
		if e != nil {
			return e
		}
		targets := p.Targets
		if target != "" {
			if !p.Supports(target) {
				return fmt.Errorf("供应商不支持指定 CLI")
			}
			targets = []string{target}
		}
		for _, t := range targets {
			if _, e = a.adapter(t); e != nil {
				return e
			}
		}
		current, e := a.current()
		if e != nil {
			return e
		}
		for _, t := range targets {
			ad, _ := a.adapter(t)
			artifact, e := ad.Render(p, a.Home, models...)
			if e != nil {
				return e
			}
			if e = ad.ApplyGlobal(artifact, nativeHome(t)); e != nil {
				return e
			}
			current[t] = p.ID
			if e = a.saveCurrent(current); e != nil {
				return e
			}
		}
		return outputJSON(cmd, current)
	}}
	cmd.Flags().StringVar(&target, "target", "", "只切换指定 CLI")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		filter, _ := cmd.Flags().GetString("target")
		return a.completeProviders(filter)(cmd, args, toComplete)
	}
	return cmd
}
