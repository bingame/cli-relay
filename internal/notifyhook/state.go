package notifyhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bingame/cli-relay/internal/safeio"
)

// State 记录 relay notify 在各 CLI 写入了哪些条目，供卸载时精确还原；
// Codex 链式接管前的原始 notify 也保存在这里。
type State struct {
	Version int    `json:"version"`
	Binary  string `json:"binary"`
	Claude  struct {
		Installed bool   `json:"installed"`
		Command   string `json:"command"`
	} `json:"claude"`
	Codex struct {
		Installed   bool     `json:"installed"`
		Previous    []string `json:"previous_notify"`
		HasPrevious bool     `json:"has_previous_notify"`
	} `json:"codex"`
	ZCode struct {
		Installed       bool  `json:"installed"`
		PreviousEnabled *bool `json:"previous_enabled"`
	} `json:"zcode"`
}

func statePath(relayHome string) string {
	return filepath.Join(relayHome, "notify-state.json")
}

// LoadState 读取安装状态；尚未安装时返回零值。
func LoadState(relayHome string) (State, error) {
	raw, err := safeio.ReadRegular(statePath(relayHome), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: 1}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if json.Unmarshal(raw, &st) != nil || st.Version != 1 {
		return State{}, fmt.Errorf("relay notify 安装状态无效")
	}
	return st, nil
}

func SaveState(relayHome string, st State) error {
	st.Version = 1
	content, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码安装状态")
	}
	return safeio.WriteFile(statePath(relayHome), append(content, '\n'), 0600)
}
