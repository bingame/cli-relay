package ccswitch

import (
	"context"
	"reflect"

	"github.com/bingame/cli-relay/internal/provider"
)

// providerOnly 与新导入共用白名单，不从旧 source_settings 恢复任何本地配置。
func providerOnly(p provider.Provider) provider.Provider {
	extra := map[string]any{}
	switch p.Target {
	case "claude":
		settings, _ := p.Extra["claude_settings"].(map[string]any)
		clean := claudeSettingsWhitelist(settings)
		env := clean["env"].(map[string]any)
		// 凭据只从加密密钥库读取，不能重新放入公开配置。
		delete(env, "ANTHROPIC_API_KEY")
		delete(env, "ANTHROPIC_AUTH_TOKEN")
		delete(env, "ANTHROPIC_CUSTOM_HEADERS")
		extra["claude_settings"] = clean
	case "codex":
		config, _ := p.Extra["codex_config"].(map[string]any)
		extra["codex_config"] = codexConfigWhitelist(config)
	}
	p.Extra = extra
	return p
}

// KeepSecret 只保留供应商主凭据和白名单配置实际引用的环境凭据。
func KeepSecret(p provider.Provider, key string) bool {
	if key == "api_key" {
		return true
	}
	if p.Target == "claude" {
		return key == "api_key_env" || key == "env:ANTHROPIC_CUSTOM_HEADERS"
	}
	if p.Target == "codex" {
		config, _ := p.Extra["codex_config"].(map[string]any)
		defs, _ := config["model_providers"].(map[string]any)
		selected, _ := defs[firstString(config["model_provider"])].(map[string]any)
		refs, _ := selected["env_http_headers"].(map[string]any)
		for _, ref := range refs {
			if name, ok := ref.(string); ok && name != "" && key == "env:"+name {
				return true
			}
		}
	}
	return false
}

// PlanCleanup 只读取元数据，dry-run 不需要解锁密钥库，也不读取历史快照内容。
func PlanCleanup(ctx context.Context, store *provider.Store) ([]provider.ImportCleanup, error) {
	items, err := store.List(ctx, "")
	if err != nil {
		return nil, err
	}
	var plans []provider.ImportCleanup
	for _, p := range items {
		if p.Source != "cc-switch-import" {
			continue
		}
		clean := providerOnly(p)
		keys, err := store.SecretNames(ctx, p.Target, p.ID)
		if err != nil {
			return nil, err
		}
		var removed []string
		for _, key := range keys {
			if !KeepSecret(clean, key) {
				removed = append(removed, key)
			}
		}
		if reflect.DeepEqual(p.Extra, clean.Extra) && len(removed) == 0 {
			continue
		}
		models, err := store.Models(ctx, p.Target, p.ID)
		if err != nil {
			return nil, err
		}
		clean.ContentHash = provider.ContentHash(clean, models)
		plans = append(plans, provider.ImportCleanup{Provider: clean, RemovedSecrets: removed, Models: models})
	}
	return plans, nil
}
