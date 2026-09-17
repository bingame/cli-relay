package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
)

const managedFile = ".relay-managed.json"

// JSON 无注释；独立记录叶子字段及最后一次写入的值，保留用户新增字段。
type managedState struct {
	Version  int            `json:"version"`
	Provider string         `json:"provider"`
	Fields   map[string]any `json:"fields"`
}

func (*Adapter) ApplyGlobal(artifact adapter.Artifact, nativeHome string) error {
	settings, err := artifactSettings(artifact)
	if err != nil {
		return err
	}
	if err := safeio.EnsureDir(nativeHome); err != nil {
		return err
	}
	lockPath := filepath.Join(nativeHome, ".relay-settings.lock")
	if err := safeio.CheckPath(lockPath); err != nil {
		return err
	}
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("无法锁定 Claude Code 配置: %w", err)
	}
	defer lock.Unlock()
	configPath := filepath.Join(nativeHome, "settings.json")
	statePath := filepath.Join(nativeHome, managedFile)
	current := map[string]any{}
	before, err := safeio.ReadRegular(configPath, 16<<20)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existed {
		if err := json.Unmarshal(before, &current); err != nil || current == nil {
			return fmt.Errorf("已有 Claude Code settings.json 不是合法 JSON 对象，未修改")
		}
	}
	state := managedState{Version: 1, Fields: map[string]any{}}
	if data, err := safeio.ReadRegular(statePath, 16<<20); err == nil {
		if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Fields == nil {
			return fmt.Errorf("Relay 的 Claude Code 管理记录无效，未修改配置")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, path := range sortedKeys(state.Fields) {
		parts, err := decodePath(path)
		if err != nil {
			return err
		}
		if existing, ok := getValue(current, parts); ok && equalJSON(existing, state.Fields[path]) {
			deleteValue(current, parts)
		}
	}
	fields := map[string]any{}
	flatten(settings, "", fields)
	for _, path := range sortedKeys(fields) {
		parts, err := decodePath(path)
		if err != nil {
			return err
		}
		if err := setValue(current, parts, fields[path]); err != nil {
			return err
		}
	}
	content, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码 Claude Code 配置")
	}
	state = managedState{Version: 1, Provider: artifact.ProviderID, Fields: fields}
	stateContent, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码 Relay 管理记录")
	}
	if err := safeio.WriteFile(configPath, append(content, '\n'), 0600); err != nil {
		return err
	}
	if err := safeio.WriteFile(statePath, append(stateContent, '\n'), 0600); err != nil {
		var rollbackErr error
		if existed {
			rollbackErr = safeio.WriteFile(configPath, before, 0600)
		} else {
			rollbackErr = os.Remove(configPath)
		}
		if rollbackErr != nil {
			return fmt.Errorf("写入管理记录失败，恢复配置也失败: %w", errors.Join(err, rollbackErr))
		}
		return fmt.Errorf("写入管理记录失败，配置已恢复: %w", err)
	}
	return nil
}

func flatten(value map[string]any, prefix string, fields map[string]any) {
	for key, child := range value {
		path := prefix + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		if object, ok := child.(map[string]any); ok && len(object) > 0 {
			flatten(object, path, fields)
		} else {
			fields[path] = child
		}
	}
}

func decodePath(path string) ([]string, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("Relay 管理记录包含无效字段路径")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func getValue(object map[string]any, parts []string) (any, bool) {
	value, ok := object[parts[0]]
	if !ok || len(parts) == 1 {
		return value, ok
	}
	child, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return getValue(child, parts[1:])
}

func deleteValue(object map[string]any, parts []string) {
	if len(parts) == 1 {
		delete(object, parts[0])
		return
	}
	child, ok := object[parts[0]].(map[string]any)
	if !ok {
		return
	}
	deleteValue(child, parts[1:])
	if len(child) == 0 {
		delete(object, parts[0])
	}
}

func setValue(object map[string]any, parts []string, value any) error {
	key := parts[0]
	if len(parts) == 1 {
		if existing, ok := object[key]; ok && !equalJSON(existing, value) {
			return fmt.Errorf("Claude Code 字段 %s 已由用户设置或修改，未覆盖", key)
		}
		object[key] = value
		return nil
	}
	child, ok := object[key].(map[string]any)
	if !ok {
		if _, exists := object[key]; exists {
			return fmt.Errorf("Claude Code 字段 %s 与 Relay 配置结构冲突，未覆盖", key)
		}
		child = map[string]any{}
		object[key] = child
	}
	return setValue(child, parts[1:], value)
}
