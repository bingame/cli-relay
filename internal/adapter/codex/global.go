package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"relay/internal/adapter"
	"relay/internal/safeio"
)

func profileContent(id string, content []byte) []byte {
	return append([]byte("# Relay 管理的 Codex profile："+id+"\n"), content...)
}

func (a *Adapter) ApplyGlobal(artifact adapter.Artifact, nativeHome string) error {
	config, id, err := artifactConfig(artifact)
	if err != nil {
		return err
	}
	if nativeHome == "" {
		nativeHome, err = a.nativeHome()
		if err != nil {
			return err
		}
	}
	if err := safeio.EnsureDir(nativeHome); err != nil {
		return err
	}
	lockPath := filepath.Join(nativeHome, ".relay-config.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("Codex 配置锁必须是常规文件")
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("无法锁定 Codex 配置：%w", err)
	}
	if !locked {
		return fmt.Errorf("Codex 配置正在被其他 Relay 进程更新")
	}
	defer lock.Unlock()
	path := filepath.Join(nativeHome, "config.toml")
	content, err := safeio.ReadRegular(path, 16<<20)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var existing map[string]any
	if err := toml.Unmarshal(content, &existing); err != nil {
		return fmt.Errorf("现有 Codex config.toml 无效，未修改文件")
	}
	base, owned, err := removeManagedBlock(content, id)
	if err != nil {
		return err
	}
	if definitions, ok := existing["model_providers"].(map[string]any); ok {
		if _, exists := definitions[id]; exists && !owned {
			return fmt.Errorf("Codex 已有同名手动供应商配置，拒绝覆盖")
		}
	}
	profilePath := filepath.Join(nativeHome, id+".config.toml")
	oldProfile, profileErr := safeio.ReadRegular(profilePath, 16<<20)
	if profileErr != nil && !os.IsNotExist(profileErr) {
		return profileErr
	}
	if profileErr == nil && !bytes.HasPrefix(oldProfile, []byte("# Relay 管理的 Codex profile："+id+"\n")) {
		return fmt.Errorf("Codex 已有同名手动 profile，拒绝覆盖")
	}
	pointers := map[string]any{}
	for key, value := range config {
		if key != "model_providers" {
			pointers[key] = value
		}
	}
	// 新版 Codex 对旧顶层 profile 指针直接报错；switch 仅移除指针，保留旧 profile 内容。
	pointers["profile"] = nil
	base, err = setTopLevelValues(base, pointers)
	if err != nil {
		return err
	}
	definition, err := toml.Marshal(map[string]any{"model_providers": config["model_providers"]})
	if err != nil {
		return fmt.Errorf("无法生成 Codex 供应商区块")
	}
	// 单个 fragment 的父表声明不能重复追加到已包含其他供应商的配置。
	definition = bytes.TrimPrefix(definition, []byte("[model_providers]\n"))
	merged := append([]byte{}, base...)
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	if len(merged) > 0 && !bytes.HasSuffix(merged, []byte("\n\n")) {
		merged = append(merged, '\n')
	}
	merged = append(merged, []byte("# BEGIN RELAY CODEX "+id+"\n")...)
	merged = append(merged, definition...)
	merged = append(merged, []byte("# END RELAY CODEX "+id+"\n")...)
	var checked map[string]any
	if err := toml.Unmarshal(merged, &checked); err != nil {
		return fmt.Errorf("合并后的 Codex 配置无效，未修改文件")
	}
	if err := safeio.WriteFile(profilePath, profileContent(id, artifact.Content), 0600); err != nil {
		return err
	}
	if err := safeio.WriteFile(path, merged, 0600); err != nil {
		return err
	}
	a.NativeHome = nativeHome
	return nil
}

// 只承认 TOML 解析器识别的整行注释，字符串中的相同文字不会被误作管理标记。
func removeManagedBlock(content []byte, id string) ([]byte, bool, error) {
	begin := "# BEGIN RELAY CODEX " + id
	end := "# END RELAY CODEX " + id
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(content)
	start, finish := -1, -1
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Comment {
			continue
		}
		text := strings.TrimSpace(string(parser.Raw(node.Raw)))
		if text != begin && text != end {
			continue
		}
		offset := int(node.Raw.Offset)
		lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
		if strings.TrimSpace(string(content[lineStart:offset])) != "" {
			continue
		}
		if text == begin {
			if start != -1 || finish != -1 {
				return nil, false, fmt.Errorf("Codex Relay 管理标记重复或嵌套")
			}
			start = lineStart
		} else {
			if start == -1 || finish != -1 {
				return nil, false, fmt.Errorf("Codex Relay 管理标记不完整")
			}
			finish = offset + int(node.Raw.Length)
			if finish < len(content) && content[finish] == '\r' {
				finish++
			}
			if finish < len(content) && content[finish] == '\n' {
				finish++
			}
		}
	}
	if parser.Error() != nil {
		return nil, false, fmt.Errorf("无法解析 Codex 原有 TOML")
	}
	if start == -1 && finish == -1 {
		return content, false, nil
	}
	if start == -1 || finish == -1 {
		return nil, false, fmt.Errorf("Codex Relay 管理标记不完整")
	}
	result := append([]byte{}, content[:start]...)
	result = append(result, content[finish:]...)
	return result, true, nil
}

type replacement struct {
	start, end int
	value      []byte
}

// 原位替换顶层指针，保留同行注释、无关设置、子表和用户排版。
func setTopLevelValues(content []byte, values map[string]any) ([]byte, error) {
	parser := unstable.Parser{}
	parser.Reset(content)
	seen := map[string]bool{}
	var edits []replacement
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			break
		}
		if node.Kind != unstable.KeyValue {
			continue
		}
		key := node.Key()
		if !key.Next() {
			continue
		}
		name := string(key.Node().Data)
		if key.Next() {
			continue
		}
		value, ok := values[name]
		if !ok {
			continue
		}
		var expression []byte
		if value != nil {
			encoded, err := inlineTOML(value)
			if err != nil {
				return nil, err
			}
			expression = []byte(name + " = " + encoded)
		}
		edits = append(edits, replacement{int(node.Raw.Offset), int(node.Raw.Offset + node.Raw.Length), expression})
		seen[name] = true
	}
	if parser.Error() != nil {
		return nil, fmt.Errorf("无法解析 Codex 顶层指针")
	}
	result := append([]byte{}, content...)
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		tail := append([]byte{}, result[e.end:]...)
		result = append(result[:e.start], e.value...)
		result = append(result, tail...)
	}
	missing := make([]string, 0)
	for key := range values {
		if !seen[key] && values[key] != nil {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	var prefix strings.Builder
	for _, key := range missing {
		value, err := inlineTOML(values[key])
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&prefix, "%s = %s\n", key, value)
	}
	return append([]byte(prefix.String()), result...), nil
}
