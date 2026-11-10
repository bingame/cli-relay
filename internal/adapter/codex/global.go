package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func profileContent(id string, profile, config map[string]any) ([]byte, error) {
	complete := make(map[string]any, len(profile)+1)
	for key, value := range profile {
		complete[key] = value
	}
	// switch 的附属 profile 本身也必须是合法配置；临时启动使用独立 CODEX_HOME。
	complete["model_providers"] = config["model_providers"]
	content, err := toml.Marshal(complete)
	if err != nil {
		return nil, fmt.Errorf("无法生成 Codex profile")
	}
	return append([]byte("# Relay 管理的 Codex profile："+id+"\n"), content...), nil
}

func (a *Adapter) ApplyGlobal(artifact adapter.Artifact, nativeHome string) error {
	config, profile, relayID, err := artifactConfig(artifact)
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
	profilePath := filepath.Join(nativeHome, relayID+".config.toml")
	oldProfile, profileErr := safeio.ReadRegular(profilePath, 16<<20)
	if profileErr != nil && !os.IsNotExist(profileErr) {
		return profileErr
	}
	if profileErr == nil && !bytes.HasPrefix(oldProfile, []byte("# Relay 管理的 Codex profile："+relayID+"\n")) {
		return fmt.Errorf("Codex 已有同名手动 profile，拒绝覆盖")
	}
	profileData, err := profileContent(relayID, profile, config)
	if err != nil {
		return err
	}
	path := filepath.Join(nativeHome, "config.toml")
	content, err := safeio.ReadRegular(path, 16<<20)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var existing map[string]any
	if err := toml.Unmarshal(content, &existing); err != nil {
		return fmt.Errorf("现有 Codex config.toml 无效，未修改文件")
	}
	nativeID, _ := names(relayID)
	// 先清掉本供应商遗留的标记行，再按 TOML 结构移除旧活动槽。
	// 标记不参与定位，cc-switch 重排注释造成的残缺不会阻断切换。
	// 旧版 Relay 按供应商 ID 命名标记，至今仍可能残留在用户配置里，
	// 因此两种命名都清理；缺失或错位都不再阻断切换。
	base, err := removeManagedMarkers(content, nativeID, relayID)
	if err != nil {
		return err
	}
	base, removed, err := removeProviderTable(base, nativeID)
	if err != nil {
		return err
	}
	if definitions, ok := existing["model_providers"].(map[string]any); ok {
		if _, exists := definitions[nativeID]; exists && !removed {
			return fmt.Errorf("Codex 的 model_providers.%s 不是可安全替换的标准表结构", nativeID)
		}
	}
	pointers := map[string]any{}
	for key, value := range profile {
		pointers[key] = value
	}
	// 切回未声明模型目录的供应商时，清除上一个全局默认留下的目录指针，
	// 让 Codex 恢复原生模型发现。
	if _, ok := pointers["model_catalog_json"]; !ok {
		pointers["model_catalog_json"] = nil
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
	// 只在追加位置收敛空行：移除管理块会留下空行，重复 switch 时若不归一，
	// 这些空行会随每次写入累积。用户文件其他位置的排版不做改动。
	merged := trimTrailingBlankLines(base)
	if len(merged) > 0 {
		merged = append(merged, '\n', '\n')
	}
	merged = append(merged, []byte("# BEGIN RELAY CODEX "+nativeID+"\n")...)
	merged = append(merged, definition...)
	merged = append(merged, []byte("# END RELAY CODEX "+nativeID+"\n")...)
	var checked map[string]any
	if err := toml.Unmarshal(merged, &checked); err != nil {
		return fmt.Errorf("合并后的 Codex 配置无效，未修改文件")
	}
	if err := safeio.WriteFile(profilePath, profileData, 0600); err != nil {
		return err
	}
	if err := safeio.WriteFile(path, merged, 0600); err != nil {
		return err
	}
	a.NativeHome = nativeHome
	return nil
}

// 只承认 TOML 解析器识别的整行注释，字符串中的相同文字不会被误作管理标记。
//
// 标记只是给人看的提示，不作为删除内容的依据。cc-switch 会整份重写 config.toml，
// 曾把成对标记拆散，留下孤立的 END，切换该 provider 时整次启动都被拒绝；更糟的是
// 一份被重排的快照可能让 BEGIN/END 包住别的供应商表。因此这里只清理给定的几组
// 标记行——成对的、孤立的、重复的一视同仁——表内容的增删一律交给
// removeProviderTable 按 TOML 结构完成，注释残缺不会误删任何配置。
// ids 同时接受当前的共享原生 ID 和旧版按供应商 ID 命名的标记。
func removeManagedMarkers(content []byte, ids ...string) ([]byte, error) {
	begin := make(map[string]bool, len(ids))
	end := make(map[string]bool, len(ids))
	for _, id := range ids {
		begin["# BEGIN RELAY CODEX "+id] = true
		end["# END RELAY CODEX "+id] = true
	}
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(content)
	var edits []replacement
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Comment {
			continue
		}
		text := strings.TrimSpace(string(parser.Raw(node.Raw)))
		if !begin[text] && !end[text] {
			continue
		}
		offset := int(node.Raw.Offset)
		lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
		if strings.TrimSpace(string(content[lineStart:offset])) != "" {
			continue
		}
		finish := offset + int(node.Raw.Length)
		if finish < len(content) && content[finish] == '\r' {
			finish++
		}
		if finish < len(content) && content[finish] == '\n' {
			finish++
		}
		edits = append(edits, replacement{start: lineStart, end: finish})
	}
	if parser.Error() != nil {
		return nil, fmt.Errorf("无法解析 Codex 原有 TOML")
	}
	return applyReplacements(content, edits), nil
}

// removeProviderTable 定点移除一个标准 TOML 表及其子表，供全局 switch 接管
// cc-switch 使用的 [model_providers.custom] 活动槽。其他 Provider、注释和排版不变。
func removeProviderTable(content []byte, id string) ([]byte, bool, error) {
	parser := unstable.Parser{}
	parser.Reset(content)
	var edits []replacement
	active := false
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			key := node.Key()
			parts := make([]string, 0, 3)
			headerEnd := 0
			for key.Next() {
				parts = append(parts, string(key.Node().Data))
				headerEnd = int(key.Node().Raw.Offset + key.Node().Raw.Length)
			}
			active = len(parts) >= 2 && parts[0] == "model_providers" && parts[1] == id
			if active {
				// Table 节点自身没有 Raw；首个键的位置位于表头所在行。
				offset := int(node.Child().Raw.Offset)
				start := bytes.LastIndexByte(content[:offset], '\n') + 1
				// 只删除表头语法，保留同行注释；从最后一个键之后找闭括号，
				// 避免把带引号键名中的 ] 当作表头结束。
				brackets := 1
				if node.Kind == unstable.ArrayTable {
					brackets = 2
				}
				for i := 0; i < brackets; i++ {
					end := bytes.IndexByte(content[headerEnd:], ']')
					if end < 0 {
						return nil, false, fmt.Errorf("无法定位 Codex Provider 表头")
					}
					headerEnd += end + 1
				}
				edits = append(edits, removeLine(content, start, headerEnd))
			}
		case unstable.KeyValue:
			if active {
				start := bytes.LastIndexByte(content[:node.Raw.Offset], '\n') + 1
				edits = append(edits, removeLine(content, start, int(node.Raw.Offset+node.Raw.Length)))
			}
		}
	}
	if parser.Error() != nil {
		return nil, false, fmt.Errorf("无法解析 Codex Provider 表")
	}
	if len(edits) == 0 {
		return content, false, nil
	}
	return applyReplacements(content, edits), true, nil
}

// removeLine 生成整行删除的替换：整行连同换行一起移除，但保留该行语法之后的
// 尾部内容（通常是同行注释），避免删除表格时把用户注释一起带走、也不留下空行。
func removeLine(content []byte, lineStart, syntaxEnd int) replacement {
	lineFinish := bytes.IndexByte(content[syntaxEnd:], '\n')
	if lineFinish < 0 {
		tail := bytes.TrimSpace(content[syntaxEnd:])
		return replacement{start: lineStart, end: len(content), value: append(tail, '\n')}
	}
	lineFinish += syntaxEnd
	tail := bytes.TrimSpace(content[syntaxEnd:lineFinish])
	if len(tail) == 0 {
		return replacement{start: lineStart, end: lineFinish + 1}
	}
	return replacement{start: lineStart, end: lineFinish + 1, value: append(tail, '\n')}
}

func applyReplacements(content []byte, edits []replacement) []byte {
	if len(edits) == 0 {
		return content
	}
	result := append([]byte{}, content...)
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		tail := append([]byte{}, result[e.end:]...)
		result = append(result[:e.start], e.value...)
		result = append(result, tail...)
	}
	return result
}

// trimTrailingBlankLines 去掉末尾的空行，让追加位置的空行数保持恒定。
func trimTrailingBlankLines(content []byte) []byte {
	trimmed := bytes.TrimRight(content, " \t\r\n")
	if len(trimmed) == len(content) {
		return content
	}
	return append([]byte{}, trimmed...)
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
