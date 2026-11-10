package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
)

// Codex 只支持一个顶层 notify 程序。relay notify install 会接管该键并记录旧值，
// hook 每次触发时由 relay 先推送再原样转发给旧程序（链式）；卸载时还原。

func notifyLock(nativeHome string) (*flock.Flock, error) {
	if err := safeio.EnsureDir(nativeHome); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(nativeHome, ".relay-config.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Codex 配置锁必须是常规文件")
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("无法锁定 Codex 配置：%w", err)
	}
	if !locked {
		return nil, fmt.Errorf("Codex 配置正在被其他 Relay 进程更新")
	}
	return lock, nil
}

func readNotify(content []byte) (current []string, present bool, err error) {
	var top map[string]any
	if err = toml.Unmarshal(content, &top); err != nil {
		return nil, false, fmt.Errorf("现有 Codex config.toml 无效，未修改文件")
	}
	raw, ok := top["notify"]
	if !ok {
		return nil, false, nil
	}
	array, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("Codex notify 不是字符串数组，未修改文件")
	}
	values := make([]string, 0, len(array))
	for _, item := range array {
		text, ok := item.(string)
		if !ok {
			return nil, true, fmt.Errorf("Codex notify 不是字符串数组，未修改文件")
		}
		values = append(values, text)
	}
	return values, true, nil
}

// NotifyIsOurs 判断 notify 值是否为 relay 安装的 hook 命令。
func NotifyIsOurs(values []string) bool {
	return strings.Contains(strings.Join(values, " "), "notify hook codex")
}

func writeNotify(content []byte, values []string) ([]byte, error) {
	merged := content
	var err error
	if values == nil {
		merged, err = setTopLevelValues(content, map[string]any{"notify": nil})
	} else {
		merged, err = setTopLevelValues(content, map[string]any{"notify": values})
	}
	if err != nil {
		return nil, err
	}
	var checked map[string]any
	if err := toml.Unmarshal(merged, &checked); err != nil {
		return nil, fmt.Errorf("合并后的 Codex 配置无效，未修改文件")
	}
	return merged, nil
}

// NotifyInspect 返回当前顶层 notify 值及是否为 relay 条目。
func NotifyInspect(nativeHome string) (current []string, ours bool, err error) {
	content, err := safeio.ReadRegular(filepath.Join(nativeHome, "config.toml"), 16<<20)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	current, present, err := readNotify(content)
	if err != nil || !present {
		return nil, false, err
	}
	return current, NotifyIsOurs(current), nil
}

// NotifyInstall 把顶层 notify 设为 relay hook 命令；返回旧值。
// replacedOther 表示接管了非 relay 的既有 notify（调用方需记入状态供还原）。
func NotifyInstall(nativeHome string, command []string) (previous []string, replacedOther bool, err error) {
	lock, err := notifyLock(nativeHome)
	if err != nil {
		return nil, false, err
	}
	defer lock.Unlock()
	path := filepath.Join(nativeHome, "config.toml")
	content, err := safeio.ReadRegular(path, 16<<20)
	if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	current, present, err := readNotify(content)
	if err != nil {
		return nil, false, err
	}
	if present && !NotifyIsOurs(current) {
		previous = current
		replacedOther = true
	}
	merged, err := writeNotify(content, command)
	if err != nil {
		return nil, false, err
	}
	if err := safeio.WriteFile(path, merged, 0600); err != nil {
		return nil, false, err
	}
	return previous, replacedOther, nil
}

// NotifyRestore 在当前 notify 仍是 relay 条目时还原为 previous（nil 表示删除该键）。
// 当前值已被用户改动时不动文件，返回 restored=false。
func NotifyRestore(nativeHome string, previous []string) (restored bool, err error) {
	lock, err := notifyLock(nativeHome)
	if err != nil {
		return false, err
	}
	defer lock.Unlock()
	path := filepath.Join(nativeHome, "config.toml")
	content, err := safeio.ReadRegular(path, 16<<20)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, present, err := readNotify(content)
	if err != nil {
		return false, err
	}
	if !present || !NotifyIsOurs(current) {
		return false, nil
	}
	merged, err := writeNotify(content, previous)
	if err != nil {
		return false, err
	}
	if err := safeio.WriteFile(path, merged, 0600); err != nil {
		return false, err
	}
	return true, nil
}
