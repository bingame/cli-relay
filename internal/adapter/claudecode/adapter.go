// Package claudecode 实现不向配置产物写入凭据的 Claude Code 启动适配器。
package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/provider"
)

type Adapter struct{ Binary string }

var _ adapter.LaunchAdapter = (*Adapter)(nil)

func New() *Adapter             { return &Adapter{Binary: "claude"} }
func (*Adapter) Target() string { return "claude" }

func (a *Adapter) Render(p provider.Provider, relayRoot string, models ...provider.Model) (adapter.Artifact, error) {
	if err := p.Validate(); err != nil {
		return adapter.Artifact{}, err
	}
	if !p.Supports(a.Target()) {
		return adapter.Artifact{}, fmt.Errorf("供应商不支持 Claude Code")
	}
	if p.EffectiveSecretMode() != "callback" {
		return adapter.Artifact{}, fmt.Errorf("Claude Code 当前仅支持 callback 密钥模式")
	}
	settings := map[string]any{}
	if raw, ok := p.Extra["claude_settings"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return adapter.Artifact{}, fmt.Errorf("Claude Code settings 无法编码")
		}
		source := map[string]any{}
		if err = json.Unmarshal(data, &source); err != nil || source == nil {
			return adapter.Artifact{}, fmt.Errorf("claude_settings 必须是 JSON 对象")
		}
		// spec §6 导入白名单：只消费用户级语义字段。permissions/hooks/
		// statusLine 等运行环境配置属于 Relay 全局配置，不进入渲染产物。
		if env, ok := source["env"]; ok {
			if values, ok := env.(map[string]any); ok {
				// 旧版 cc-switch 可能把凭据放在 env 中；Relay 统一改用
				// apiKeyHelper，并清除继承的 ANTHROPIC_AUTH_TOKEN/API_KEY，
				// 避免 Claude Code 报告鉴权来源冲突。
				delete(values, "ANTHROPIC_AUTH_TOKEN")
				delete(values, "ANTHROPIC_API_KEY")
			}
			settings["env"] = env
		}
	}
	if err := validateSettings(settings); err != nil {
		return adapter.Artifact{}, err
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if p.BaseURL != "" {
		env["ANTHROPIC_BASE_URL"] = p.BaseURL
	}
	if p.Model != "" {
		settings["model"] = p.Model
	}
	if len(env) > 0 {
		settings["env"] = env
	}
	relayHome, err := filepath.Abs(relayRoot)
	if err != nil {
		return adapter.Artifact{}, fmt.Errorf("无法解析 Relay 数据目录: %w", err)
	}
	// 回调绑定渲染时的数据目录：Multica/relay-codex 等外部启动方式可能不带 --home，
	// 不传则会解析到默认 ~/.relay，同名供应商下可能读错密钥。
	settings["apiKeyHelper"] = "relay --home " + shellQuote(relayHome) + " secret get claude " + p.ID
	if len(models) > 0 {
		options := make([]map[string]any, 0, len(models))
		for _, model := range models {
			option := map[string]any{"value": model.ModelID}
			if model.DisplayName != "" {
				option["label"] = model.DisplayName
			}
			options = append(options, option)
		}
		settings["modelPicker"] = map[string]any{"options": options, "replaceBuiltInOptions": true}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return adapter.Artifact{}, fmt.Errorf("无法编码 Claude Code 配置")
	}
	data = append(data, '\n')
	sourceHash := provider.ContentHash(p, models)
	path, err := filepath.Abs(filepath.Join(relayRoot, "rendered", a.Target(), p.ID+".json"))
	if err != nil {
		return adapter.Artifact{}, fmt.Errorf("无法解析渲染路径: %w", err)
	}
	if err := adapter.WriteRendered(path, data, sourceHash); err != nil {
		return adapter.Artifact{}, err
	}
	if err := installHandoffPlugin(filepath.Join(filepath.Dir(path), "handoff-plugin")); err != nil {
		return adapter.Artifact{}, err
	}
	return adapter.Artifact{Target: a.Target(), ProviderID: p.ID, Path: path, Content: data, Config: settings, SourceHash: sourceHash}, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellQuote 按 POSIX 单引号规则转义，供 apiKeyHelper 这类由 shell 解析的回调命令使用。
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'" + `\` + "''") + "'"
}

// 清除继承的身份、模型与第三方路由，避免上一供应商污染本次启动。
var providerEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_SMALL_FAST_MODEL_AWS_REGION",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

func (a *Adapter) BuildLaunchInputs(artifact adapter.Artifact, _ adapter.ResolvedSecrets) (adapter.LaunchInputs, error) {
	_, err := artifactSettings(artifact)
	if err != nil {
		return adapter.LaunchInputs{}, err
	}
	if artifact.Path == "" {
		return adapter.LaunchInputs{}, fmt.Errorf("Claude Code 渲染文件路径不能为空")
	}
	binary := a.Binary
	if binary == "" {
		binary = "claude"
	}
	pluginDir := filepath.Join(filepath.Dir(artifact.Path), "handoff-plugin")
	return adapter.LaunchInputs{
		Binary:   binary,
		Args:     []string{"--setting-sources", "", "--plugin-dir", pluginDir, "--settings", artifact.Path},
		Env:      map[string]string{},
		UnsetEnv: append([]string(nil), providerEnv...),
	}, nil
}

func (*Adapter) NativeArgs(mode adapter.Mode, args []string) ([]string, error) {
	if mode != adapter.Headless && mode != adapter.Interactive {
		return nil, fmt.Errorf("未知的 Claude Code 启动模式")
	}
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if name == "--settings" || name == "--setting-sources" {
			return nil, fmt.Errorf("%s 由 Relay 管理，请通过供应商配置设置", name)
		}
		if mode == adapter.Interactive {
			remaining = append(remaining, arg)
			continue
		}
		switch name {
		case "-p", "--print", "--verbose":
			if hasValue {
				return nil, fmt.Errorf("%s 不接受参数值", name)
			}
		case "--output-format":
			if !hasValue {
				i++
				if i >= len(args) {
					return nil, fmt.Errorf("--output-format 缺少参数")
				}
				value = args[i]
			}
			if value != "stream-json" {
				return nil, fmt.Errorf("relay exec 要求 --output-format stream-json")
			}
		default:
			remaining = append(remaining, arg)
		}
	}
	if mode == adapter.Headless {
		return append([]string{"-p", "--verbose", "--output-format", "stream-json"}, remaining...), nil
	}
	return remaining, nil
}

func (*Adapter) ResumeArgs(id string) []string { return []string{"--resume", id} }

func artifactSettings(artifact adapter.Artifact) (map[string]any, error) {
	if artifact.Target != "claude" || !provider.ValidID(artifact.ProviderID) {
		return nil, fmt.Errorf("无效的 Claude Code 渲染产物")
	}
	var settings map[string]any
	if len(artifact.Content) != 0 {
		if err := json.Unmarshal(artifact.Content, &settings); err != nil {
			return nil, fmt.Errorf("Claude Code 渲染产物不是合法 JSON")
		}
	} else {
		data, err := json.Marshal(artifact.Config)
		if err != nil {
			return nil, fmt.Errorf("无法读取 Claude Code 配置")
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, fmt.Errorf("无法读取 Claude Code 配置")
		}
	}
	if settings == nil {
		return nil, fmt.Errorf("Claude Code 配置必须是对象")
	}
	if err := validateSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func validateSettings(settings map[string]any) error {
	if err := rejectCredentialFields(settings); err != nil {
		return err
	}
	if value, exists := settings["env"]; exists {
		env, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("Claude Code settings.env 必须是对象")
		}
		for name, value := range env {
			if _, ok := value.(string); !ok || !envName.MatchString(name) {
				return fmt.Errorf("Claude Code settings.env 必须是合法变量名到字符串的映射")
			}
		}
	}
	return nil
}

func rejectCredentialFields(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "apiKeyHelper" {
				if helper, ok := child.(string); !ok || !strings.HasPrefix(helper, "relay --home ") || !strings.Contains(helper, " secret get claude ") {
					return fmt.Errorf("Claude Code apiKeyHelper 必须由 Relay 管理")
				}
				continue
			}
			upper := strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
			for _, marker := range []string{"API_KEY", "APIKEY", "_TOKEN", "ACCESS_KEY", "SECRET", "PASSWORD", "PRIVATE_KEY", "AUTHORIZATION", "CUSTOM_HEADERS", "CREDENTIAL"} {
				if strings.Contains(upper, marker) {
					return fmt.Errorf("Claude Code 配置不能包含凭据字段；请使用加密凭据存储")
				}
			}
			if err := rejectCredentialFields(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rejectCredentialFields(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func canonical(value any) []byte { encoded, _ := json.Marshal(value); return encoded }
func equalJSON(a, b any) bool    { return bytes.Equal(canonical(a), canonical(b)) }
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
