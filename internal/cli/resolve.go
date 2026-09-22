package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/provider"
	"github.com/spf13/cobra"
)

// resolveProvider 接受供应商 ID 或显示名称；ID 精确匹配优先，
// 名称重名时报错并列出候选 ID（spec §4：所有 provider 参数同时接受两种写法）。
func (a *App) resolveProvider(ctx context.Context, s *provider.Store, input string) (provider.Provider, error) {
	providers, err := s.List(ctx, "")
	if err != nil {
		return provider.Provider{}, err
	}
	named := []provider.Provider{}
	for _, p := range providers {
		if p.ID == input {
			return p, nil
		}
		if p.DisplayName == input {
			named = append(named, p)
		}
	}
	switch len(named) {
	case 0:
		return provider.Provider{}, fmt.Errorf("未找到供应商: %s（可传 ID 或显示名称，relay provider list 查看）", input)
	case 1:
		return named[0], nil
	}
	ids := make([]string, 0, len(named))
	for _, p := range named {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return provider.Provider{}, fmt.Errorf("显示名称 %q 对应多个供应商，请改用 ID: %s", input, strings.Join(ids, ", "))
}

// completeProviders 为 provider 参数提供 shell 补全候选：ID（描述为显示名称）
// 和不含空白的显示名称（描述为 ID）。补全失败必须静默，不能干扰 shell。
func (a *App) completeProviders(target string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		none := cobra.ShellCompDirectiveNoFileComp
		if _, err := os.Stat(filepath.Join(a.Home, "providers.db")); err != nil {
			return nil, none
		}
		// 补全可能在未执行过的命令上被调用；nil context 会让 database/sql panic。
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		s, err := a.store(cmd, false)
		if err != nil {
			return nil, none
		}
		defer s.Close()
		providers, err := s.List(ctx, target)
		if err != nil {
			return nil, none
		}
		out := make([]string, 0, len(providers)*2)
		for _, p := range providers {
			out = append(out, p.ID+"\t"+p.DisplayName)
			if name := p.DisplayName; name != "" && name != p.ID && !strings.ContainsAny(name, " \t") {
				out = append(out, name+"\t"+p.ID)
			}
		}
		return out, none
	}
}

// completeTargets 补全已注册的目标 CLI 名称。
func (a *App) completeTargets() func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		targets := make([]string, 0, len(a.Adapters))
		for target := range a.Adapters {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		return targets, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeProviderArg 供「<cli> <provider_id>」形式的位置参数使用：
// 第一个参数补目标 CLI，第二个按目标 CLI 过滤补供应商。
func (a *App) completeProviderArg() func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return a.completeTargets()(cmd, args, toComplete)
		case 1:
			return a.completeProviders(args[0])(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}
