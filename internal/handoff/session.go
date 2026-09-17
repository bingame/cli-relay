package handoff

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"relay/internal/provider"
	"relay/internal/safeio"
)

// FindSessionFile 仅搜索显式 nativeHome 下的原生会话目录，不扫描用户磁盘。
func FindSessionFile(cli, id, nativeHome string) (string, error) {
	if !provider.ValidID(id) {
		return "", fmt.Errorf("会话 ID 含非法路径字符")
	}
	var subdir string
	switch cli {
	case "claude-code":
		subdir = "projects"
	case "codex":
		subdir = "sessions"
	default:
		return "", fmt.Errorf("尚不支持该 CLI 的原始会话查找")
	}
	root, err := filepath.Abs(filepath.Join(nativeHome, subdir))
	if err != nil {
		return "", err
	}
	if err := safeio.CheckPath(root); err != nil {
		return "", err
	}
	var found string
	count := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 50000 {
			return fmt.Errorf("会话目录超过 50000 项，请缩小原生目录范围")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("会话路径超出原生目录")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= 8 {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		matches := name == id+".jsonl"
		if cli == "codex" {
			matches = strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+id+".jsonl") || name == id+".jsonl"
		}
		if !matches {
			return nil
		}
		if err := safeio.CheckPath(path); err != nil {
			return err
		}
		if found != "" {
			return fmt.Errorf("发现多个同 ID 会话文件，请指定明确的原始会话路径")
		}
		found = path
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("在原生会话目录中未找到指定会话")
	}
	return found, nil
}

func GitInfo(workdir string) GitMetadata {
	info := GitMetadata{WorkDir: workdir}
	if path, err := filepath.Abs(workdir); err == nil {
		info.WorkDir = path
	}
	if err := safeio.CheckPath(info.WorkDir); err != nil {
		return info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", info.WorkDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if head, ok := run("rev-parse", "--verify", "HEAD"); ok {
		info.HeadCommit = head
	}
	if branch, ok := run("symbolic-ref", "--quiet", "--short", "HEAD"); ok {
		info.Branch = branch
	}
	if status, ok := run("status", "--porcelain=v1", "--untracked-files=normal"); ok {
		info.Dirty = status != ""
	}
	return info
}
