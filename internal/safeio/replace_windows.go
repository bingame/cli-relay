//go:build windows

package safeio

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceFile 使用 MOVEFILE_REPLACE_EXISTING，并对 Windows 瞬时文件占用重试。
func replaceFile(from, to string) error {
	// Codex、杀毒软件或索引器可能在极短时间内以不允许删除/重命名
	// 的共享方式打开目标文件。让原子替换避开这个瞬时占用窗口。
	for attempt := 0; ; attempt++ {
		err := os.Rename(from, to)
		if err == nil || !replaceRetryable(err) || attempt >= 5 {
			return err
		}
		time.Sleep(time.Duration(20*(1<<attempt)) * time.Millisecond)
	}
}

func replaceRetryable(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
