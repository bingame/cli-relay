package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/importer/ccswitch"
	"github.com/bingame/cli-relay/internal/provider"
	"github.com/spf13/cobra"
)

func (a *App) providerCommand() *cobra.Command {
	parent := &cobra.Command{Use: "provider", Short: "管理、导入和渲染供应商"}
	var hardPrune bool
	var p provider.Provider
	var keyStdin bool
	add := &cobra.Command{Use: "add", Short: "添加供应商，凭据通过标准输入读取", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if p.DisplayName == "" {
			p.DisplayName = p.ID
		}
		p.Source = "manual"
		if e := p.Validate(); e != nil {
			return e
		}
		sec := map[string]string{}
		if keyStdin {
			b, e := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
			if e != nil {
				return e
			}
			key := strings.TrimRight(string(b), "\r\n")
			if key == "" || len(b) > 65536 || strings.ContainsAny(key, "\r\n\x00") {
				return fmt.Errorf("标准输入必须是一条非空 API key")
			}
			sec["api_key"] = key
		}
		unlock, e := a.lock(cmd.Context())
		if e != nil {
			return e
		}
		defer unlock()
		store, e := a.store(cmd, len(sec) > 0)
		if e != nil {
			return e
		}
		defer store.Close()
		existing, err := store.List(cmd.Context(), "")
		if err != nil {
			return err
		}
		for _, entry := range existing {
			if entry.ID == p.ID {
				return fmt.Errorf("供应商 ID 已存在: %s", p.ID)
			}
		}
		// 手工 `provider add --model X` 是用户显式声明，写成一条模型目录记录；
		// 这与导入路径"源里没声明就一条都不写"（spec §5.2）是刻意的非对称。
		models := []provider.Model{}
		if p.Model != "" {
			models = append(models, provider.Model{ProviderID: p.ID, ModelID: p.Model, IsDefault: true})
		}
		for _, target := range p.Targets {
			if ad, ok := a.Adapters[target]; ok {
				if _, e = ad.Render(p, a.Home, models...); e != nil {
					return e
				}
			}
		}
		if e = store.Import(cmd.Context(), []provider.Entry{{Provider: p, Secrets: sec, Models: models}}, "", nil); e != nil {
			return e
		}
		return outputJSON(cmd, p)
	}}
	add.Flags().StringVar(&p.ID, "id", "", "供应商 ID")
	add.Flags().StringVar(&p.DisplayName, "name", "", "显示名称")
	add.Flags().StringSliceVar(&p.Targets, "target", nil, "目标 CLI，可重复或逗号分隔")
	add.Flags().StringVar(&p.BaseURL, "base-url", "", "API 地址")
	add.Flags().StringVar(&p.Model, "model", "", "模型")
	add.Flags().StringVar(&p.SecretMode, "secret-mode", "callback", "密钥模式：callback、env_key 或 env_inline")
	add.Flags().BoolVar(&keyStdin, "api-key-stdin", false, "从标准输入读取 API key")
	_ = add.MarkFlagRequired("id")
	_ = add.MarkFlagRequired("target")
	var target string
	list := &cobra.Command{Use: "list", Short: "列出供应商（不输出凭据）", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if _, e := os.Stat(filepath.Join(a.Home, "providers.db")); os.IsNotExist(e) {
			return outputJSON(cmd, []provider.Provider{})
		}
		s, e := a.store(cmd, false)
		if e != nil {
			return e
		}
		defer s.Close()
		p, e := s.List(cmd.Context(), target)
		if e != nil {
			return e
		}
		return outputJSON(cmd, p)
	}}
	list.Flags().StringVar(&target, "target", "", "按目标 CLI 筛选")
	remove := &cobra.Command{Use: "remove <provider>", Short: "删除未激活的供应商及加密凭据", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		current, e := a.current()
		if e != nil {
			return e
		}
		for _, id := range current {
			if id == p.ID {
				return fmt.Errorf("供应商仍被全局默认引用，请先 switch 到其他供应商")
			}
		}
		if e = s.Remove(cmd.Context(), p.ID); e != nil {
			return e
		}
		for t := range a.Adapters {
			paths := []string{filepath.Join(a.Home, "rendered", t, p.ID+".json")}
			if t == "codex" {
				paths = []string{filepath.Join(a.Home, "rendered", t, p.ID+".toml.fragment"), filepath.Join(a.Home, "rendered", t, p.ID+".catalog.json")}
			}
			for _, path := range paths {
				for _, candidate := range []string{path, path + ".relay-cache.json"} {
					if e = os.Remove(candidate); e != nil && !os.IsNotExist(e) {
						return e
					}
				}
			}
		}
		return nil
	}}
	remove.ValidArgsFunction = a.completeProviders("")
	prune := &cobra.Command{Use: "prune --hard", Short: "物理删除已禁用的供应商", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !hardPrune {
			return fmt.Errorf("必须显式指定 --hard")
		}
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
		count, e := s.HardPrune(cmd.Context())
		if e != nil {
			return e
		}
		return outputJSON(cmd, map[string]any{"removed": count})
	}}
	prune.Flags().BoolVar(&hardPrune, "hard", false, "物理删除全部 disabled 记录")
	parent.AddCommand(add, list, remove, prune, a.importCommand(), a.renderCommand(false), a.renderCommand(true))
	return parent
}
func (a *App) importCommand() *cobra.Command {
	var source string
	var dry bool
	var onConflict string
	var prune bool
	cmd := &cobra.Command{Use: "import <file.sql>", Short: "从 cc-switch SQL 导入（不自动切换）", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if source != "cc-switch" {
			return fmt.Errorf("仅支持 --from cc-switch")
		}
		if onConflict != "overwrite" && onConflict != "skip" {
			return fmt.Errorf("--on-conflict 必须为 overwrite 或 skip")
		}
		result, e := ccswitch.Parse(cmd.Context(), args[0])
		if e != nil {
			return e
		}
		var store *provider.Store
		existing := map[string]provider.Provider{}
		if !dry {
			unlock, e := a.lock(cmd.Context())
			if e != nil {
				return e
			}
			defer unlock()
			store, e = a.store(cmd, true)
			if e != nil {
				return e
			}
			defer store.Close()
		} else if _, e = os.Stat(filepath.Join(a.Home, "providers.db")); e == nil {
			store, e = provider.OpenReadOnly(a.Home)
			if e != nil {
				return e
			}
			defer store.Close()
		}
		if store != nil {
			items, e := store.List(cmd.Context(), "")
			if e != nil {
				return e
			}
			for _, p := range items {
				existing[p.ID] = p
			}
		}
		entries := make([]provider.Entry, 0, len(result.Providers))
		seen := make([]string, 0, len(result.Providers))
		used := map[string]bool{}
		for id := range existing {
			used[id] = true
		}
		report := []map[string]any{}
		for _, entry := range result.Providers {
			p := entry.Provider
			original := p.ID
			target := p.Targets[0]
			match := ""
			for id, old := range existing {
				if old.Source == "cc-switch-import" && old.Supports(target) && provider.Slugify(old.DisplayName) == provider.Slugify(p.DisplayName) {
					match = id
					break
				}
			}
			if match != "" {
				p.ID = match
			} else if used[p.ID] {
				// spec §6：显示名 slug 被任何记录占用时，base 回退为
				// slugify(display_name + "-" + target)，仍占用则从 -2 递增数字后缀。
				base := provider.Slugify(strings.Join([]string{p.DisplayName, target}, "-"))
				p.ID = base
				for n := 2; used[p.ID]; n++ {
					p.ID = fmt.Sprintf("%s-%d", base, n)
				}
			}
			used[p.ID] = true
			for i := range entry.Models {
				entry.Models[i].ProviderID = p.ID
			}
			seen = append(seen, p.ID)
			row := map[string]any{"id": p.ID, "original_id": entry.OriginalID, "display_name": p.DisplayName, "targets": p.Targets, "was_current": entry.Current}
			if len(entry.Warnings) > 0 {
				row["warnings"] = entry.Warnings
			}
			if p.ID != original {
				row["conflict_renamed"] = true
			}
			if old, exists := existing[p.ID]; exists && old.Source == "cc-switch-import" && onConflict == "skip" {
				row["skipped"] = true
				report = append(report, row)
				continue
			}
			unsupported := []string{}
			for _, t := range p.Targets {
				if _, ok := a.Adapters[t]; !ok {
					unsupported = append(unsupported, t)
				}
			}
			if len(unsupported) > 0 {
				row["unsupported_targets"] = unsupported
			}
			if entry.Current {
				row["suggestion"] = "relay switch " + p.ID
			}
			report = append(report, row)
			entries = append(entries, provider.Entry{Provider: p, Secrets: entry.Secrets, Models: entry.Models})
		}
		stale := []string{}
		seenSet := map[string]bool{}
		for _, id := range seen {
			seenSet[id] = true
		}
		for id, old := range existing {
			if old.Source == "cc-switch-import" && !seenSet[id] {
				stale = append(stale, id)
			}
		}
		sort.Strings(stale)
		if !dry {
			for _, entry := range entries {
				for _, target := range entry.Provider.Targets {
					if ad, ok := a.Adapters[target]; ok {
						if _, e = ad.Render(entry.Provider, a.Home, entry.Models...); e != nil {
							return fmt.Errorf("渲染供应商 %s 失败: %w", entry.Provider.ID, e)
						}
					}
				}
			}
			for _, entry := range entries {
				if e = store.Upsert(cmd.Context(), entry); e != nil {
					return e
				}
			}
			if e = store.RecordImport(cmd.Context(), result.FileHash, result.Snapshot, seen); e != nil {
				return e
			}
			if prune {
				if _, e = store.SetDisabledExcept(cmd.Context(), seen); e != nil {
					return e
				}
			}
		}
		return outputJSON(cmd, map[string]any{"dry_run": dry, "count": len(report), "providers": report, "stale_providers": stale})
	}}
	cmd.Flags().StringVar(&source, "from", "", "导入来源")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "仅解析和报告，不写入本地存储")
	cmd.Flags().StringVar(&onConflict, "on-conflict", "overwrite", "同名供应商处理：overwrite 或 skip")
	cmd.Flags().BoolVar(&prune, "prune", false, "将导出中已不存在的 cc-switch 供应商标记为 disabled")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}
func (a *App) renderCommand(env bool) *cobra.Command {
	name := "render-args"
	if env {
		name = "render-env"
	}
	format := "dotenv"
	cmd := &cobra.Command{Use: name + " <cli> <provider>", Short: "输出原生启动片段；render-env 是敏感输出", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if env && format != "dotenv" && format != "json" {
			return fmt.Errorf("format 必须为 dotenv 或 json")
		}
		ad, e := a.adapter(args[0])
		if e != nil {
			return e
		}
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
		p, e := a.resolveProvider(cmd.Context(), s, args[1])
		if e != nil {
			return e
		}
		if !p.Supports(args[0]) {
			return fmt.Errorf("供应商不支持指定 CLI")
		}
		if p.EffectiveStatus() == "disabled" {
			return fmt.Errorf("供应商已被 cc-switch 同步标记为失效: %s", p.ID)
		}
		models, e := s.Models(cmd.Context(), p.ID)
		if e != nil {
			return e
		}
		artifact, e := ad.Render(p, a.Home, models...)
		if e != nil {
			return e
		}
		sec := map[string]string{}
		if env {
			if _, e = os.Stat(filepath.Join(a.Home, "vault.json")); e == nil {
				v, e := a.vault(cmd)
				if e != nil {
					return e
				}
				s.SetCipher(v)
			}
			sec, e = s.Secrets(cmd.Context(), p.ID)
			if e != nil {
				return e
			}
		}
		inputs, e := ad.BuildLaunchInputs(artifact, adapter.ResolvedSecrets(sec))
		if e != nil {
			return e
		}
		if !env {
			return outputJSON(cmd, inputs.Args)
		}
		if inputs.Env == nil {
			inputs.Env = map[string]string{}
		}
		if format == "json" {
			return outputJSON(cmd, inputs.Env)
		}
		keys := make([]string, 0, len(inputs.Env))
		for k, v := range inputs.Env {
			if strings.ContainsAny(k, "=\r\n\x00") || strings.ContainsAny(v, "\r\n\x00") {
				return fmt.Errorf("dotenv 输出不支持换行或 NUL，请使用 --format json")
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, e = fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, inputs.Env[k]); e != nil {
				return e
			}
		}
		return nil
	}}
	if env {
		cmd.Flags().StringVar(&format, "format", "dotenv", "dotenv 或 json（Multica 需要 json）")
	}
	cmd.ValidArgsFunction = a.completeProviderArg()
	return cmd
}
