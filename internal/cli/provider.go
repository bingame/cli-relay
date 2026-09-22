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
		existing, err := store.List(cmd.Context(), p.Target)
		if err != nil {
			return err
		}
		for _, entry := range existing {
			if entry.ID == p.ID {
				return fmt.Errorf("供应商 ID 已存在: %s", p.ID)
			}
			if entry.DisplayName == p.DisplayName {
				return fmt.Errorf("同一 CLI（%s）下显示名称已存在: %s", p.Target, p.DisplayName)
			}
		}
		// 手工 `provider add --model X` 是用户显式声明，写成一条模型目录记录；
		// 这与导入路径"源里没声明就一条都不写"（spec §5.2）是刻意的非对称。
		models := []provider.Model{}
		if p.Model != "" {
			models = append(models, provider.Model{ProviderID: p.ID, ModelID: p.Model, IsDefault: true})
		}
		if ad, ok := a.Adapters[p.Target]; ok {
			if _, e = ad.Render(p, a.Home, models...); e != nil {
				return e
			}
		}
		if e = store.Import(cmd.Context(), []provider.Entry{{Provider: p, Secrets: sec, Models: models}}, "", nil); e != nil {
			return e
		}
		return outputJSON(cmd, p)
	}}
	add.Flags().StringVar(&p.ID, "id", "", "供应商 ID")
	add.Flags().StringVar(&p.DisplayName, "name", "", "显示名称")
	add.Flags().StringVar(&p.Target, "target", "", "目标 CLI")
	add.Flags().StringVar(&p.BaseURL, "base-url", "", "API 地址")
	add.Flags().StringVar(&p.Model, "model", "", "模型")
	add.Flags().StringVar(&p.SecretMode, "secret-mode", "callback", "密钥模式：callback、env_key 或 env_inline")
	add.Flags().BoolVar(&keyStdin, "api-key-stdin", false, "从标准输入读取 API key")
	_ = add.RegisterFlagCompletionFunc("target", a.completeTargets())
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
	_ = list.RegisterFlagCompletionFunc("target", a.completeTargets())
	removeTarget := ""
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
		p, e := a.resolveProvider(cmd.Context(), s, removeTarget, args[0])
		if e != nil {
			return e
		}
		current, e := a.current()
		if e != nil {
			return e
		}
		if current[p.Target] == p.ID {
			return fmt.Errorf("供应商仍被全局默认引用，请先 switch 到其他供应商")
		}
		if e = s.Remove(cmd.Context(), p.Target, p.ID); e != nil {
			return e
		}
		t := p.Target
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
		return nil
	}}
	remove.ValidArgsFunction = a.completeProviders("")
	remove.Flags().StringVar(&removeTarget, "target", "", "限定供应商所属 CLI（跨 CLI 同名时使用）")
	_ = remove.RegisterFlagCompletionFunc("target", a.completeTargets())
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
				existing[providerKey(p.Target, p.ID)] = p
			}
		}
		entries := make([]provider.Entry, 0, len(result.Providers))
		seenByTarget := map[string][]string{}
		seenSet := map[string]map[string]bool{}
		report := []map[string]any{}
		for _, entry := range result.Providers {
			p := entry.Provider
			for i := range entry.Models {
				entry.Models[i].ProviderID = p.ID
			}
			seenByTarget[p.Target] = append(seenByTarget[p.Target], p.ID)
			if seenSet[p.Target] == nil {
				seenSet[p.Target] = map[string]bool{}
			}
			seenSet[p.Target][p.ID] = true
			row := map[string]any{"id": p.ID, "original_id": entry.OriginalID, "display_name": p.DisplayName, "target": p.Target, "was_current": entry.Current}
			if len(entry.Warnings) > 0 {
				row["warnings"] = entry.Warnings
			}
			if _, exists := existing[providerKey(p.Target, p.ID)]; exists && onConflict == "skip" {
				row["skipped"] = true
				report = append(report, row)
				continue
			}
			if _, ok := a.Adapters[p.Target]; !ok {
				row["unsupported_target"] = p.Target
			}
			if entry.Current {
				row["suggestion"] = "relay switch " + p.ID
			}
			report = append(report, row)
			entries = append(entries, provider.Entry{Provider: p, Secrets: entry.Secrets, Models: entry.Models})
		}
		stale := []string{}
		for _, old := range existing {
			if old.Source != "cc-switch-import" {
				continue
			}
			if !seenSet[old.Target][old.ID] {
				stale = append(stale, old.Target+"/"+old.ID)
			}
		}
		sort.Strings(stale)
		if !dry {
			for _, entry := range entries {
				if ad, ok := a.Adapters[entry.Provider.Target]; ok {
					if _, e = ad.Render(entry.Provider, a.Home, entry.Models...); e != nil {
						return fmt.Errorf("渲染供应商 %s 失败: %w", entry.Provider.ID, e)
					}
				}
			}
			for _, entry := range entries {
				if e = store.Upsert(cmd.Context(), entry); e != nil {
					return e
				}
			}
			if e = store.RecordImport(cmd.Context(), result.FileHash, result.Snapshot, allSeen(seenByTarget)); e != nil {
				return e
			}
			if prune {
				for target := range a.Adapters {
					if _, e = store.SetDisabledExcept(cmd.Context(), target, seenByTarget[target]); e != nil {
						return e
					}
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
		p, e := a.resolveProvider(cmd.Context(), s, args[0], args[1])
		if e != nil {
			return e
		}
		if !p.Supports(args[0]) {
			return fmt.Errorf("供应商不支持指定 CLI")
		}
		if p.EffectiveStatus() == "disabled" {
			return fmt.Errorf("供应商已被 cc-switch 同步标记为失效: %s", p.ID)
		}
		models, e := s.Models(cmd.Context(), p.Target, p.ID)
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
			sec, e = s.Secrets(cmd.Context(), p.Target, p.ID)
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

// providerKey 生成 (target, id) 的复合键，用于跨 CLI 的本地索引；
// NUL 不出现在合法 ID 中，因此不会与普通字符串冲突。
func providerKey(target, id string) string { return target + "\x00" + id }

func allSeen(byTarget map[string][]string) []string {
	out := []string{}
	for _, ids := range byTarget {
		out = append(out, ids...)
	}
	sort.Strings(out)
	return out
}
