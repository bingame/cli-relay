package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
)

// scopedState 只记录生成值的摘要，用于区分上游配置更新和 TUI 保存的用户偏好。
type scopedState struct {
	NativeHome string            `json:"native_home"`
	Defaults   map[string]string `json:"defaults"`
}

func valueHash(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func scopedHomePath(artifact adapter.Artifact, nativeHome string) string {
	// 不同 Relay 数据库可以含同名供应商，不能共用同一份配置。
	root, _ := filepath.Abs(filepath.Dir(artifact.Path))
	digest := sha256.Sum256([]byte(root))
	idDigest := sha256.Sum256([]byte(artifact.ProviderID))
	return filepath.Join(nativeHome, ".relay", fmt.Sprintf("%x", digest[:8]), fmt.Sprintf("%s-%x", artifact.ProviderID, idDigest[:6]))
}

func prepareScopedHome(artifact adapter.Artifact, nativeHome string) (string, map[string]string, error) {
	config, profile, _, err := artifactConfig(artifact)
	if err != nil {
		return "", nil, err
	}
	nativeHome, err = filepath.Abs(nativeHome)
	if err != nil {
		return "", nil, err
	}
	// 嵌套 relay run 不继续嵌套隔离目录，始终继承最初的用户配置与会话目录。
	if data, readErr := safeio.ReadRegular(filepath.Join(nativeHome, ".relay-home.json"), 1<<20); readErr == nil {
		var state scopedState
		if json.Unmarshal(data, &state) != nil || !filepath.IsAbs(state.NativeHome) {
			return "", nil, fmt.Errorf("Relay Codex 隔离目录标记无效")
		}
		nativeHome = state.NativeHome
	} else if !os.IsNotExist(readErr) {
		return "", nil, readErr
	}
	home := scopedHomePath(artifact, nativeHome)
	if err := safeio.EnsurePrivateDir(home); err != nil {
		return "", nil, err
	}
	lockPath := filepath.Join(home, ".relay-config.lock")
	if err := safeio.CheckPath(lockPath); err != nil {
		return "", nil, err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return "", nil, fmt.Errorf("Codex 隔离配置正在被更新或无法锁定")
	}
	defer lock.Unlock()

	base, err := safeio.ReadRegular(filepath.Join(nativeHome, "config.toml"), 16<<20)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	merged := map[string]any{}
	if toml.Unmarshal(base, &merged) != nil {
		return "", nil, fmt.Errorf("现有 Codex config.toml 无效，未修改文件")
	}
	// Provider 表整体替换，旧 token、auth、headers 和 env_key 均不进入新文件。
	delete(merged, "model_providers")
	delete(merged, "profile")
	delete(merged, "profiles")
	delete(merged, "model_catalog_json")
	// 普通模型选项仍继承原生配置；只有显式声明的供应商默认覆盖它们。
	for key, value := range profile {
		merged[key] = value
	}
	merged["model_providers"] = config["model_providers"]
	resolveConfigPaths(merged, nativeHome)

	env := map[string]string{}
	if err := liftMCPSecrets(merged, artifact.ProviderID, env); err != nil {
		return "", nil, err
	}
	if inheritedCredentialLiteral(merged) {
		return "", nil, fmt.Errorf("原生 Codex 配置包含无法安全继承的明文凭据，请改用环境引用或回调")
	}
	// 共享所有 SQLite 数据库（含新版 thread_history），不用逐个链接 WAL 文件。
	sqliteHome, _ := merged["sqlite_home"].(string)
	if sqliteHome == "" {
		sqliteHome = nativeHome
	}
	if inherited := os.Getenv("CODEX_SQLITE_HOME"); inherited != "" {
		sqliteHome = inherited
	}
	merged["sqlite_home"] = sqliteHome
	env["CODEX_SQLITE_HOME"] = sqliteHome
	state := scopedState{NativeHome: nativeHome, Defaults: map[string]string{}}
	for key, value := range merged {
		state.Defaults[key] = valueHash(value)
	}
	statePath := filepath.Join(home, ".relay-home.json")
	previous, err := safeio.ReadRegular(statePath, 1<<20)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	var oldState scopedState
	if err == nil && (json.Unmarshal(previous, &oldState) != nil || oldState.NativeHome != nativeHome) {
		return "", nil, fmt.Errorf("Relay Codex 隔离配置元数据无效")
	}
	path := filepath.Join(home, "config.toml")
	old, readErr := safeio.ReadRegular(path, 16<<20)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", nil, readErr
	}
	if readErr == nil {
		if oldState.Defaults == nil {
			return "", nil, fmt.Errorf("Codex 隔离目录存在未受 Relay 管理的配置，拒绝覆盖")
		}
		var saved map[string]any
		if toml.Unmarshal(old, &saved) != nil {
			return "", nil, fmt.Errorf("Codex 隔离配置无效，拒绝覆盖")
		}
		// 仅当继承/供应商默认未改变时保留 TUI 的修改；显式修改 Provider 默认仍生效。
		for key, hash := range oldState.Defaults {
			if key != "model" && key != "model_reasoning_effort" {
				continue
			}
			if state.Defaults[key] == hash {
				if value, ok := saved[key]; ok {
					merged[key] = value
				} else {
					delete(merged, key)
				}
			}
		}
		for _, key := range []string{"model", "model_reasoning_effort"} {
			if value, exists := saved[key]; exists && oldState.Defaults[key] == "" && state.Defaults[key] == "" {
				merged[key] = value
			}
		}
	}
	if err := shareNativeState(nativeHome, home); err != nil {
		return "", nil, err
	}
	data, err := toml.Marshal(merged)
	if err != nil {
		return "", nil, fmt.Errorf("无法生成 Codex 隔离配置")
	}
	if !reflect.DeepEqual(old, data) {
		if err := safeio.WriteFile(path, data, 0600); err != nil {
			return "", nil, err
		}
	}
	stateData, _ := json.Marshal(state)
	if err := safeio.WriteFile(statePath, stateData, 0600); err != nil {
		return "", nil, err
	}
	return home, env, nil
}

// 相对路径原本以原生配置目录为基准，移动配置后必须保持其含义。
func resolveConfigPaths(config map[string]any, root string) {
	pathKeys := map[string]bool{"model_instructions_file": true, "model_catalog_json": true, "config_file": true, "sqlite_home": true, "log_dir": true, "cwd": true, "path": true}
	resolve := func(path string) string {
		if path != "" && !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			return filepath.Join(root, path)
		}
		return path
	}
	var visit func(map[string]any)
	visit = func(values map[string]any) {
		for key, value := range values {
			if path, ok := value.(string); ok && pathKeys[key] {
				values[key] = resolve(path)
			}
			if child, ok := value.(map[string]any); ok {
				visit(child)
			}
			if children, ok := value.([]any); ok {
				for _, child := range children {
					if fields, ok := child.(map[string]any); ok {
						visit(fields)
					}
				}
			}
		}
		if values["source_type"] == "local" {
			if source, ok := values["source"].(string); ok {
				values["source"] = resolve(source)
			}
		}
	}
	visit(config)
}

func inheritedCredentialLiteral(value any) bool {
	switch fields := value.(type) {
	case map[string]any:
		for key, child := range fields {
			if text, ok := child.(string); ok && text != "" {
				lower := strings.ToLower(key)
				// env 引用是变量名；授权策略和能力开关不是凭据。
				if lower != "env_key" && lower != "env_key_instructions" && !strings.HasSuffix(lower, "_credentials_store") && !strings.HasSuffix(lower, "_env_var") && !strings.HasSuffix(lower, "_env") && containsCredentials(map[string]any{key: text}, true) {
					return true
				}
			}
			if key != "env_http_headers" && inheritedCredentialLiteral(child) {
				return true
			}
		}
	case []any:
		for _, child := range fields {
			if inheritedCredentialLiteral(child) {
				return true
			}
		}
	}
	return false
}

// 继承 MCP 时不复制静态凭据：改写为 Codex 的环境引用，值只在子进程内存中传递。
func liftMCPSecrets(config map[string]any, id string, env map[string]string) error {
	servers, _ := config["mcp_servers"].(map[string]any)
	for name, raw := range servers {
		server, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		vars, _ := server["env_vars"].([]any)
		if values, ok := server["env"].(map[string]any); ok {
			for key, rawValue := range values {
				if !containsCredentials(map[string]any{key: rawValue}) {
					continue
				}
				value, ok := rawValue.(string)
				if !ok || !envName.MatchString(key) || strings.ContainsRune(value, 0) {
					return fmt.Errorf("MCP 环境变量无效")
				}
				if prior, exists := env[key]; exists && prior != value {
					return fmt.Errorf("多个 MCP 的同名环境变量不同，无法安全继承：%s", key)
				}
				env[key] = value
				vars = append(vars, key)
				delete(values, key)
			}
			if len(vars) > 0 {
				server["env_vars"] = vars
			}
			if len(values) == 0 {
				delete(server, "env")
			}
		}
		if headers, ok := server["http_headers"].(map[string]any); ok {
			refs, _ := server["env_http_headers"].(map[string]any)
			if refs == nil {
				refs = map[string]any{}
			}
			for header, rawValue := range headers {
				value, ok := rawValue.(string)
				if !ok || strings.ContainsRune(value, 0) {
					return fmt.Errorf("MCP header 无效")
				}
				key := fmt.Sprintf("RELAY_MCP_%X", sha256.Sum256([]byte(id+"/"+name+"/"+header)))
				env[key], refs[header] = value, key
			}
			server["env_http_headers"] = refs
			delete(server, "http_headers")
		}
	}
	return nil
}

func shareNativeState(nativeHome, home string) error {
	// daemon/socket/tmp/配置保持隔离；插件缓存、规则与会话引用同一份原生文件。
	for _, name := range []string{"sessions", "archived_sessions", "skills", "plugins", "rules", "memories", "attachments", "thread-writer-locks", "worktrees"} {
		source := filepath.Join(nativeHome, name)
		if info, err := os.Stat(source); err == nil && info.IsDir() {
			// 用户已有的 Skills/会话链接可以沿用；只在源不存在时创建目录。
		} else if err := safeio.EnsureDir(source); err != nil {
			return err
		}
		if err := ensureSharedPath(source, filepath.Join(home, name), true); err != nil {
			return err
		}
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md", "hooks.json", "auth.json", ".credentials.json", "history.jsonl", "session_index.jsonl"} {
		source := filepath.Join(nativeHome, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			if name != "history.jsonl" && name != "session_index.jsonl" {
				// 原目录注销或删除指令后，不能继续使用此前留下的硬链接。
				destination := filepath.Join(home, name)
				if info, err := os.Lstat(destination); err == nil {
					if info.IsDir() {
						return fmt.Errorf("Codex 共享文件被目录替换：%s", destination)
					}
					if err := os.Remove(destination); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				}
				continue
			}
			// 预先建立空历史索引，确保首次隔离启动也向原生目录保存。
			f, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil && !os.IsExist(err) {
				return err
			}
			if err == nil {
				f.Close()
			}
		} else if err != nil {
			return err
		}
		if err := ensureSharedPath(source, filepath.Join(home, name), false); err != nil {
			return err
		}
	}
	return nil
}

func ensureSharedPath(source, destination string, directory bool) error {
	want, err := os.Stat(source)
	if err != nil {
		return err
	}
	if got, err := os.Stat(destination); err == nil {
		if os.SameFile(want, got) {
			return nil
		}
		// 原生应用可能原子替换文件；Windows 硬链接须在下次启动重新绑定。
		if !directory && runtime.GOOS == "windows" {
			if info, err := os.Lstat(destination); err == nil && info.Mode().IsRegular() {
				if err := os.Remove(destination); err != nil {
					return err
				}
				return linkNativePath(source, destination, false)
			}
		}
		return fmt.Errorf("Codex 共享路径已被替换，拒绝覆盖：%s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	return linkNativePath(source, destination, directory)
}
