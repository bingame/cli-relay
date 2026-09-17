package skills

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAndUpgradePreserveUserChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "relay-handoff")
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, handoff) {
		t.Fatal("安装内容不正确", err)
	}
	old := []byte("旧版本指引\n")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".relay-sha256"), []byte(fmt.Sprintf("%x\n", sha256.Sum256(old))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir); err != nil {
		t.Fatal("升级失败", err)
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(got, handoff) {
		t.Fatal("没有更新旧版本")
	}
	custom := []byte("用户修改过的指引")
	if err := os.WriteFile(path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir); err == nil {
		t.Fatal("覆盖了用户修改")
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(got, custom) {
		t.Fatal("失败后修改了用户文件")
	}
}

func TestInstallRejectsUnmanagedFileAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("自定义 Skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir); err == nil {
		t.Fatal("覆盖了非 Relay 文件")
	}
	linkDir := t.TempDir()
	if err := os.Symlink(path, filepath.Join(linkDir, "SKILL.md")); err != nil {
		t.Skip("当前环境不能创建符号链接")
	}
	if err := Install(linkDir); err == nil {
		t.Fatal("接受了符号链接")
	}
}
