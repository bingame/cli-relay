//go:build !windows

package safeio

import "os"

// replaceFile 用同目录 rename 原子替换目标文件。
func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
