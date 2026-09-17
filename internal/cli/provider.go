package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"relay/internal/adapter"
	"relay/internal/importer/ccswitch"
	"relay/internal/provider"
)

func (a *App) providerCommand() *cobra.Command {
	parent := &cobra.Command{Use: "provider", Short: "管理、导入和渲染供应商"}
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
		for _, target := range p.Targets {
			if ad, ok := a.Adapters[target]; ok {
				if _, e = ad.Render(p, a.Home); e != nil {
					return e
				}
			}
		}
		if e = store.Add(cmd.Context(), p, sec); e != nil {
			return e
		}
		return outputJSON(cmd, p)
	}}
	add.Flags().StringVar(&p.ID, "id", "", "供应商 ID")
	add.Flags().StringVar(&p.DisplayName, "name", "", "显示名称")
	add.Flags().StringSliceVar(&p.Targets, "target", nil, "目标 CLI，可重复或逗号分隔")
	add.Flags().StringVar(&p.BaseURL, "base-url", "", "API 地址")
	add.Flags().StringVar(&p.Model, "model", "", "模型")
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
	remove := &cobra.Command{Use: "remove <id>", Short: "删除未激活的供应商及加密凭据", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !provider.ValidID(args[0]) {
			return fmt.Errorf("无效供应商 ID")
		}
		unlock, e := a.lock(cmd.Context())
		if e != nil {
			return e
		}
		defer unlock()
		current, e := a.current()
		if e != nil {
			return e
		}
		for _, id := range current {
			if id == args[0] {
				return fmt.Errorf("供应商仍被全局默认引用，请先 switch 到其他供应商")
			}
		}
		s, e := a.store(cmd, false)
		if e != nil {
			return e
		}
		defer s.Close()
		if e = s.Remove(cmd.Context(), args[0]); e != nil {
			return e
		}
		for t := range a.Adapters {
			ext := ".json"
			if t == "codex" {
				ext = ".toml.fragment"
			}
			path := filepath.Join(a.Home, "rendered", t, args[0]+ext)
			if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		return nil
	}}
	parent.AddCommand(add, list, remove, a.importCommand(), a.renderCommand(false), a.renderCommand(true))
	return parent
}
func (a *App) importCommand() *cobra.Command {
	var source string
	var dry bool
	cmd := &cobra.Command{Use: "import <file.sql>", Short: "从 cc-switch SQL 导入（不自动切换）", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if source != "cc-switch" {
			return fmt.Errorf("仅支持 --from cc-switch")
		}
		result, e := ccswitch.Parse(cmd.Context(), args[0])
		if e != nil {
			return e
		}
		var store *provider.Store
		ids := map[string]bool{}
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
				ids[p.ID] = true
			}
		}
		entries := make([]provider.Entry, 0, len(result.Providers))
		report := []map[string]any{}
		for _, entry := range result.Providers {
			p := entry.Provider
			original := p.ID
			for n := 1; ids[p.ID]; n++ {
				base := original
				if len(base) > 100 {
					base = base[:100]
				}
				p.ID = fmt.Sprintf("%s-imported-%d", base, n)
			}
			ids[p.ID] = true
			row := map[string]any{"id": p.ID, "original_id": entry.OriginalID, "display_name": p.DisplayName, "targets": p.Targets, "was_current": entry.Current}
			if len(entry.Warnings) > 0 {
				row["warnings"] = entry.Warnings
			}
			if p.ID != original {
				row["conflict_renamed"] = true
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
			entries = append(entries, provider.Entry{Provider: p, Secrets: entry.Secrets})
		}
		if !dry {
			for _, entry := range entries {
				for _, target := range entry.Provider.Targets {
					if ad, ok := a.Adapters[target]; ok {
						if _, e = ad.Render(entry.Provider, a.Home); e != nil {
							return fmt.Errorf("渲染供应商 %s 失败: %w", entry.Provider.ID, e)
						}
					}
				}
			}
			if e = store.Import(cmd.Context(), entries, result.FileHash, result.Snapshot); e != nil {
				return e
			}
		}
		return outputJSON(cmd, map[string]any{"dry_run": dry, "count": len(report), "providers": report})
	}}
	cmd.Flags().StringVar(&source, "from", "", "导入来源")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "仅解析和报告，不写入本地存储")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}
func (a *App) renderCommand(env bool) *cobra.Command {
	name := "render-args"
	if env {
		name = "render-env"
	}
	format := "dotenv"
	cmd := &cobra.Command{Use: name + " <cli> <provider_id>", Short: "输出原生启动片段；render-env 是敏感输出", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
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
		p, e := s.Get(cmd.Context(), args[1])
		if e != nil {
			return e
		}
		if !p.Supports(args[0]) {
			return fmt.Errorf("供应商不支持指定 CLI")
		}
		artifact, e := ad.Render(p, a.Home)
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
	return cmd
}
