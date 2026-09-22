package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/provider"
	"github.com/pelletier/go-toml/v2"
)

// Adapter 使用原生参数和环境启动 Codex；默认无需安装全局配置。
type Adapter struct {
	Binary     string
	LaunchMode string
	NativeHome string
}

func New() *Adapter               { return &Adapter{Binary: "codex", LaunchMode: "override"} }
func (a *Adapter) Target() string { return "codex" }

var _ adapter.LaunchAdapter = (*Adapter)(nil)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func names(id string) (string, string) {
	digest := sha256.Sum256([]byte(id))
	slug := strings.ReplaceAll(id, "-", "_")
	return id, fmt.Sprintf("RELAY_%s_%X_KEY", strings.ToUpper(slug), digest[:6])
}

var providerFields = map[string]bool{
	"name": true, "base_url": true, "wire_api": true, "requires_openai_auth": true,
	"env_key_instructions": true, "query_params": true, "env_http_headers": true,
	"request_max_retries": true, "stream_max_retries": true, "stream_idle_timeout_ms": true,
	"supports_websockets": true, "websocket_connect_timeout_ms": true,
}

var profileFields = map[string]bool{
	"model": true, "model_reasoning_effort": true, "model_reasoning_summary": true,
	"model_verbosity": true, "model_context_window": true, "model_auto_compact_token_limit": true,
	"model_supports_reasoning_summaries": true,
}

var integerFields = map[string]bool{
	"request_max_retries": true, "stream_max_retries": true, "stream_idle_timeout_ms": true,
	"websocket_connect_timeout_ms": true, "model_context_window": true, "model_auto_compact_token_limit": true,
}

// Extra 从 SQLite 的 JSON 列读取时数字默认是 float64；原生 TOML 仍必须保留整数类型。
func normalizeIntegers(config map[string]any) error {
	for key, value := range config {
		if !integerFields[key] {
			continue
		}
		var integer int64
		switch number := value.(type) {
		case int:
			integer = int64(number)
		case int64:
			integer = number
		case json.Number:
			var err error
			integer, err = number.Int64()
			if err != nil {
				return fmt.Errorf("Codex %s 必须是非负整数", key)
			}
		case float64:
			if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < 0 || number > 1<<53 {
				return fmt.Errorf("Codex %s 必须是可精确表示的非负整数", key)
			}
			integer = int64(number)
		default:
			return fmt.Errorf("Codex %s 必须是非负整数", key)
		}
		if integer < 0 {
			return fmt.Errorf("Codex %s 必须是非负整数", key)
		}
		config[key] = integer
	}
	return nil
}

func containsCredentials(value any, allowAuth ...bool) bool {
	authAllowed := len(allowAuth) > 0 && allowAuth[0]
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			k := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			normalized := strings.ReplaceAll(k, "_", "")
			if k != "env_key" && k != "env_key_instructions" && k != "env_http_headers" {
				if k == "auth" && !authAllowed || k == "http_headers" || normalized == "token" || strings.Contains(normalized, "authorization") || strings.Contains(normalized, "apikey") || strings.Contains(normalized, "password") || strings.Contains(normalized, "passphrase") || strings.Contains(normalized, "bearer") || strings.HasSuffix(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "credential") || strings.Contains(normalized, "privatekey") || strings.Contains(normalized, "cookie") {
					return true
				}
			}
			if k == "env_http_headers" {
				// 映射中的值是环境变量名，不是 HTTP header 的字面值。
				continue
			}
			if containsCredentials(child, authAllowed) {
				return true
			}
		}
	case map[string]string:
		m := make(map[string]any, len(v))
		for k, s := range v {
			m[k] = s
		}
		return containsCredentials(m, authAllowed)
	case []any:
		for _, child := range v {
			if containsCredentials(child, authAllowed) {
				return true
			}
		}
	}
	return false
}

func (a *Adapter) Render(p provider.Provider, relayRoot string, models ...provider.Model) (adapter.Artifact, error) {
	if err := p.Validate(); err != nil {
		return adapter.Artifact{}, err
	}
	if !p.Supports(a.Target()) {
		return adapter.Artifact{}, fmt.Errorf("供应商不支持 Codex")
	}
	nativeID, envKey := names(p.ID)
	definition := map[string]any{"name": p.DisplayName, "wire_api": "responses"}
	profile := map[string]any{"model_provider": nativeID}
	if raw, ok := p.Extra["codex_config"]; ok {
		config, ok := raw.(map[string]any)
		if !ok {
			return adapter.Artifact{}, fmt.Errorf("codex_config 必须是解析后的 TOML 对象")
		}
		if containsCredentials(config) {
			return adapter.Artifact{}, fmt.Errorf("Codex 原生配置包含凭据字段，请使用加密密钥库")
		}
		for k, v := range config {
			if profileFields[k] {
				profile[k] = v
			}
		}
		selected, _ := config["model_provider"].(string)
		if defs, ok := config["model_providers"].(map[string]any); ok && selected != "" {
			if original, ok := defs[selected].(map[string]any); ok {
				for k, v := range original {
					if providerFields[k] {
						definition[k] = v
					}
				}
			}
		}
	}
	if p.BaseURL != "" {
		definition["base_url"] = p.BaseURL
	}
	if p.Model != "" {
		profile["model"] = p.Model
	}
	switch p.EffectiveSecretMode() {
	case "callback":
		definition["auth"] = map[string]any{
			"command": "relay", "args": []string{"secret", "get", "codex", p.ID},
			"timeout_ms": int64(5000), "refresh_interval_ms": int64(0),
		}
	case "env_key":
		definition["env_key"] = envKey
	default:
		return adapter.Artifact{}, fmt.Errorf("Codex 仅支持 callback 或 env_key 密钥模式")
	}
	if err := normalizeIntegers(definition); err != nil {
		return adapter.Artifact{}, err
	}
	if err := normalizeIntegers(profile); err != nil {
		return adapter.Artifact{}, err
	}
	if raw, ok := definition["base_url"]; ok {
		baseURL, ok := raw.(string)
		if !ok {
			return adapter.Artifact{}, fmt.Errorf("Codex base_url 必须是字符串")
		}
		validation := p
		validation.BaseURL = baseURL
		if err := validation.Validate(); err != nil {
			return adapter.Artifact{}, err
		}
	}
	if headers, ok := definition["env_http_headers"]; ok {
		m, ok := headers.(map[string]any)
		if !ok {
			return adapter.Artifact{}, fmt.Errorf("Codex env_http_headers 必须是环境变量名映射")
		}
		for _, value := range m {
			name, ok := value.(string)
			if !ok || !envName.MatchString(name) {
				return adapter.Artifact{}, fmt.Errorf("Codex env_http_headers 包含无效环境变量名")
			}
		}
	}
	config := map[string]any{"model_providers": map[string]any{nativeID: definition}}
	sourceHash := provider.ContentHash(p, models)
	content, err := toml.Marshal(config)
	if err != nil {
		return adapter.Artifact{}, fmt.Errorf("无法生成 Codex 原生配置")
	}
	content = append([]byte("# relay-source-hash: "+sourceHash+"\n"), content...)
	path := filepath.Join(relayRoot, "rendered", a.Target(), p.ID+".toml.fragment")
	if err := adapter.WriteRendered(path, content, sourceHash); err != nil {
		return adapter.Artifact{}, err
	}
	// 只有供应商**声明过模型目录**（provider_models 非空）才写 model_catalog_json：
	// 设置该配置项后 Codex 不再拉取 provider 的 /v1/models，凭空造一份单条目录会
	// 把"任意模型"渠道锁死成一个模型。只有默认模型时交给 Codex 自己发现模型列表
	// （与 cc-switch 一致：模型映射留空就不生成 catalog）。
	if len(models) > 0 {
		catalogPath := filepath.Join(relayRoot, "rendered", a.Target(), p.ID+".catalog.json")
		catalog, err := buildCatalog(p, models)
		if err != nil {
			return adapter.Artifact{}, err
		}
		// 没有可用模型时宁可不写 catalog，也不交给 Codex 一个空目录。
		if catalog != nil {
			data, err := json.MarshalIndent(catalog, "", "  ")
			if err != nil {
				return adapter.Artifact{}, fmt.Errorf("无法生成 Codex 模型目录")
			}
			if err := adapter.WriteRendered(catalogPath, append(data, '\n'), sourceHash); err != nil {
				return adapter.Artifact{}, err
			}
			profile["model_catalog_json"] = catalogPath
		}
	}
	if _, ok := profile["model_catalog_json"]; !ok {
		// 供应商不再声明模型目录（或目录空到无法渲染）时清掉上一次留下的 catalog
		// 文件：产物目录是 Relay 自己的缓存，留着无人引用的旧目录只会误导排查。
		if err := adapter.RemoveRendered(filepath.Join(relayRoot, "rendered", a.Target(), p.ID+".catalog.json")); err != nil {
			return adapter.Artifact{}, fmt.Errorf("无法清理陈旧的 Codex 模型目录: %w", err)
		}
	}
	return adapter.Artifact{Target: a.Target(), ProviderID: p.ID, Path: path, Content: content, EnvKey: envKey, Config: config, Profile: profile, SourceHash: sourceHash}, nil
}

// artifactConfig 每次从不可含密钥的 TOML 重新校验，避免调用方修改 Config 后绕过边界。
func artifactConfig(artifact adapter.Artifact) (map[string]any, map[string]any, string, error) {
	if artifact.Target != "codex" || !provider.ValidID(artifact.ProviderID) {
		return nil, nil, "", fmt.Errorf("无效的 Codex 配置产物")
	}
	id, envKey := names(artifact.ProviderID)
	if artifact.EnvKey != envKey {
		return nil, nil, "", fmt.Errorf("Codex 配置产物的环境变量名不匹配")
	}
	var config map[string]any
	if err := toml.Unmarshal(artifact.Content, &config); err != nil {
		return nil, nil, "", fmt.Errorf("Codex 配置产物不是有效 TOML")
	}
	if containsCredentials(config, true) {
		return nil, nil, "", fmt.Errorf("Codex 配置产物包含凭据字段")
	}
	profile := artifact.Profile
	if profile == nil || profile["model_provider"] != id {
		return nil, nil, "", fmt.Errorf("Codex 配置产物的供应商指针不匹配")
	}
	defs, ok := config["model_providers"].(map[string]any)
	if !ok || len(defs) != 1 {
		return nil, nil, "", fmt.Errorf("Codex 配置产物缺少独立供应商定义")
	}
	def, ok := defs[id].(map[string]any)
	if !ok {
		return nil, nil, "", fmt.Errorf("Codex 配置产物缺少供应商定义")
	}
	_, callback := def["auth"].(map[string]any)
	if !callback && def["env_key"] != envKey {
		return nil, nil, "", fmt.Errorf("Codex 配置产物缺少有效鉴权配置")
	}
	return config, profile, id, nil
}

func (a *Adapter) BuildLaunchInputs(artifact adapter.Artifact, secrets adapter.ResolvedSecrets) (adapter.LaunchInputs, error) {
	config, _, id, err := artifactConfig(artifact)
	if err != nil {
		return adapter.LaunchInputs{}, err
	}
	binary := a.Binary
	if binary == "" {
		binary = "codex"
	}
	inputs := adapter.LaunchInputs{Binary: binary, Env: map[string]string{}, UnsetEnv: []string{artifact.EnvKey}}
	def := config["model_providers"].(map[string]any)[id].(map[string]any)
	if def["env_key"] == artifact.EnvKey {
		if key := secrets["api_key"]; key != "" {
			inputs.Env[artifact.EnvKey] = key
		}
	}
	for k, v := range secrets {
		if !strings.HasPrefix(k, "env:") {
			continue
		}
		name := strings.TrimPrefix(k, "env:")
		if !envName.MatchString(name) || name == artifact.EnvKey {
			return adapter.LaunchInputs{}, fmt.Errorf("密钥包含无效或保留的环境变量名")
		}
		if strings.ContainsRune(v, 0) {
			return adapter.LaunchInputs{}, fmt.Errorf("密钥包含环境变量不支持的字符")
		}
		inputs.Env[name] = v
	}
	if strings.ContainsRune(inputs.Env[artifact.EnvKey], 0) {
		return adapter.LaunchInputs{}, fmt.Errorf("密钥包含环境变量不支持的字符")
	}
	home, err := a.nativeHome()
	if err != nil {
		return adapter.LaunchInputs{}, err
	}
	if err := a.install(artifact, home, false); err != nil {
		return adapter.LaunchInputs{}, err
	}
	inputs.Args = []string{"--profile", id}
	return inputs, nil
}

func inlineTOML(value any) (string, error) {
	var out bytes.Buffer
	encoder := toml.NewEncoder(&out).SetTablesInline(true)
	if err := encoder.Encode(map[string]any{"value": value}); err != nil {
		return "", fmt.Errorf("Codex 配置值无法转换为原生参数")
	}
	_, encoded, ok := strings.Cut(strings.TrimSpace(out.String()), "=")
	if !ok {
		return "", fmt.Errorf("无法构造 Codex 配置参数")
	}
	return strings.TrimSpace(encoded), nil
}

func (a *Adapter) nativeHome() (string, error) {
	if a.NativeHome != "" {
		return a.NativeHome, nil
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func (a *Adapter) ResumeArgs(id string) []string { return []string{"resume", id} }

func commandIndex(args []string) int {
	valueFlags := map[string]bool{"-c": true, "--config": true, "-p": true, "--profile": true, "-m": true, "--model": true, "-C": true, "--cd": true, "-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true, "--enable": true, "--disable": true, "-i": true, "--image": true, "--add-dir": true, "--local-provider": true, "--remote": true, "--remote-auth-token-env": true}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return -1
		}
		if valueFlags[args[i]] {
			i++
			continue
		}
		if !strings.HasPrefix(args[i], "-") {
			return i
		}
	}
	return -1
}

// IsProtocolBridge 区分长期双向 RPC 与单次无头回合；调用方应保留 RPC 的原生退出语义。
func (a *Adapter) IsProtocolBridge(args []string) bool {
	index := commandIndex(args)
	return index >= 0 && args[index] == "app-server"
}

func (a *Adapter) NativeArgs(mode adapter.Mode, args []string) ([]string, error) {
	result := append([]string{}, args...)
	if mode == adapter.Interactive {
		return result, nil
	}
	if mode != adapter.Headless {
		return nil, fmt.Errorf("不支持的 Codex 进程模式")
	}
	index := commandIndex(result)
	if a.IsProtocolBridge(result) {
		return result, nil
	}
	if index < 0 || (result[index] != "exec" && result[index] != "e") {
		result = append([]string{"exec"}, result...)
	}
	insert := len(result)
	for i, arg := range result {
		if arg == "--" {
			insert = i
			break
		}
		if arg == "--json" {
			return result, nil
		}
	}
	result = append(result, "")
	copy(result[insert+1:], result[insert:])
	result[insert] = "--json"
	return result, nil
}
