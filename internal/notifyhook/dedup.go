package notifyhook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bingame/cli-relay/internal/safeio"
)

// dedupWindow 内同一 session 的同类事件只推送一次，防止 hook 短时间重复触发。
const dedupWindow = 10 * time.Second

const lastFile = "notify-last.json"

// AllowOnce 记录并判断事件是否越过去重窗口；读写失败时放行（宁重勿漏）。
func AllowOnce(relayHome, key string, now time.Time) bool {
	path := filepath.Join(relayHome, lastFile)
	entries := map[string]int64{}
	if raw, err := safeio.ReadRegular(path, 1<<20); err == nil {
		_ = json.Unmarshal(raw, &entries)
	}
	kept := map[string]int64{}
	for k, ts := range entries {
		if now.Sub(time.Unix(ts, 0)) < 60*time.Second {
			kept[k] = ts
		}
	}
	if ts, ok := kept[key]; ok && now.Sub(time.Unix(ts, 0)) < dedupWindow {
		return false
	}
	kept[key] = now.Unix()
	content, err := json.Marshal(kept)
	if err != nil {
		return true
	}
	_ = os.WriteFile(path, append(content, '\n'), 0600)
	return true
}

// LogError 把 hook 发送失败追加到 notify.log；超过 1MB 时截断。
// 只记录错误事实，不记录任何密钥内容。
func LogError(relayHome, format string, args ...any) {
	line := time.Now().Format("2006-01-02T15:04:05") + " " + fmt.Sprintf(format, args...) + "\n"
	path := filepath.Join(relayHome, "notify.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// LogTail 返回 notify.log 的末尾内容，供 status 诊断。
func LogTail(relayHome string, max int) string {
	raw, err := safeio.ReadRegular(filepath.Join(relayHome, "notify.log"), 1<<20)
	if err != nil || len(raw) == 0 {
		return ""
	}
	if len(raw) > max {
		raw = raw[len(raw)-max:]
		if idx := strings.LastIndexByte(string(raw[:max/2]), '\n'); idx >= 0 {
			raw = raw[idx+1:]
		}
	}
	return string(raw)
}
