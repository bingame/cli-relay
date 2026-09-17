// Package skills 将交接指引随二进制分发，安装时不依赖网络。
package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/gofrs/flock"
)

//go:embed relay-handoff/SKILL.md
var handoff []byte

// Install 的 targetDir 是 relay-handoff 目录本身。只更新 Relay 管理且未经手改的文件。
func Install(targetDir string) error {
	if targetDir == "" {
		return fmt.Errorf("Skill 安装目录不能为空")
	}
	if err := safeio.EnsureDir(targetDir); err != nil {
		return err
	}
	lockPath := filepath.Join(targetDir, ".relay-install.lock")
	if err := safeio.CheckPath(lockPath); err != nil {
		return err
	}
	lock := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("等待 Skill 安装锁超时")
	}
	defer func() { _ = lock.Unlock(); _ = lock.Close() }()
	path := filepath.Join(targetDir, "SKILL.md")
	stamp := filepath.Join(targetDir, ".relay-sha256")
	previous, err := safeio.ReadRegular(path, 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !bytes.Equal(previous, handoff) {
		managed, readErr := safeio.ReadRegular(stamp, 128)
		if readErr != nil || string(bytes.TrimSpace(managed)) != fmt.Sprintf("%x", sha256.Sum256(previous)) {
			return fmt.Errorf("保留已存在或被修改的 Skill: %s；请备份并移走该文件后重试", path)
		}
	}
	if err := safeio.CheckPath(stamp); err != nil {
		return err
	}
	if !bytes.Equal(previous, handoff) {
		if err := safeio.WriteFile(path, handoff, 0600); err != nil {
			return err
		}
	}
	return safeio.WriteFile(stamp, []byte(fmt.Sprintf("%x\n", sha256.Sum256(handoff))), 0600)
}
