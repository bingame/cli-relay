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
// target 非空时只在指定 CLI 内匹配，空字符串表示跨 CLI 匹配（此时同名不同 CLI 会报候选）。
func (a *App) resolveProvider(ctx context.Context, s *provider.Store, target, input string) (provider.Provider, error) {
	providers, err := s.List(ctx, target)
	if err != nil {
		return provider.Provider{}, err
	}
	named := []provider.Provider{}
	exact := []provider.Provider{}
	for _, p := range providers {
		if p.ID == input {
			exact = append(exact, p)
			continue
		}
		if p.DisplayName == input {
			named = append(named, p)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = named
	}
	switch len(candidates) {
	case 0:
		return provider.Provider{}, fmt.Errorf("未找到供应商: %s（可传 ID 或显示名称，relay provider list 查看；跨 CLI 同名时请用 --target 或 <cli> 限定）", input)
	case 1:
		return candidates[0], nil
	}
	ids := make([]string, 0, len(candidates))
	for _, p := range candidates {
		ids = append(ids, p.Target+"/"+p.ID)
	}
	sort.Strings(ids)
	return provider.Provider{}, fmt.Errorf("%q 对应多个 CLI 的供应商，请改用 --target 或 CLI 参数限定: %s", input, strings.Join(ids, ", "))
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

// completeTargetArg 只补第一个位置参数里的目标 CLI；后续参数通常是
// 传给原生 CLI 的参数，不应继续注入 relay 的 CLI 候选。
func (a *App) completeTargetArg() func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.completeTargets()(cmd, args, toComplete)
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
