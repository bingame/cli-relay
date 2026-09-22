package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateSkills(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	c := filepath.Join(root, "claude")
	x := filepath.Join(root, "codex")
	t.Setenv("CLAUDE_CONFIG_DIR", c)
	t.Setenv("CODEX_HOME", x)
	t.Setenv("RELAY_CLAUDE_BIN", filepath.Join(root, "missing-claude"))
	t.Setenv("RELAY_CODEX_BIN", filepath.Join(root, "missing-codex"))
	return filepath.Join(root, "relay"), c, x
}

func TestSkillInstallExplicitOfflineAndIdempotent(t *testing.T) {
	root, c, x := isolateSkills(t)
	for range 2 {
		out, err := command(t, root, "", "skill", "install", "--cli", "claude,codex,codex")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(out, "已安装") != 2 {
			t.Fatal(out)
		}
	}
	for _, path := range []string{c, x} {
		b, err := os.ReadFile(filepath.Join(path, "skills", "relay-handoff", "SKILL.md"))
		if err != nil || !strings.Contains(string(b), "name: relay-handoff") {
			t.Fatal(string(b), err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("Skill 安装不应初始化数据库或凭据库")
	}
}

func TestSkillInstallDetectionAndValidation(t *testing.T) {
	root, c, x := isolateSkills(t)
	out, err := command(t, root, "", "skill", "install")
	if err != nil || !strings.Contains(out, "未探测到") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		t.Fatal("未安装的 CLI 被修改")
	}
	// 用测试进程自身作可发现的入口；探测不能执行它。
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_CODEX_BIN", binary)
	if _, err := command(t, root, "", "skill", "install", "--cli", "claude,typo"); err == nil {
		t.Fatal("接受了未知 CLI")
	}
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		t.Fatal("参数校验前发生部分安装")
	}
	if _, err := command(t, root, "", "skill", "install"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(x, "skills", "relay-handoff", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		t.Fatal("安装了未探测到的 Claude")
	}
}

func TestSkillInstallReportsConflictAndContinuesOtherTarget(t *testing.T) {
	root, c, x := isolateSkills(t)
	dir := filepath.Join(c, "skills", "relay-handoff")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("手动定制"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := command(t, root, "", "skill", "install", "--cli", "claude,codex")
	if err == nil || !strings.Contains(err.Error(), "保留") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(x, "skills", "relay-handoff", "SKILL.md")); err != nil {
		t.Fatal("一个目标失败不应跳过其他目标", err)
	}
}
