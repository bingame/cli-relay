package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveBinary 避免把不受信任的原生参数交给 Windows cmd.exe 再解析。
func ResolveBinary(binary string) (string, error) {
	path, e := exec.LookPath(binary)
	if e != nil {
		return "", e
	}
	if runtime.GOOS != "windows" {
		return path, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" && ext != ".ps1" {
		return path, nil
	}
	if strings.EqualFold(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "codex") {
		arch := "x64"
		triple := "x86_64-pc-windows-msvc"
		if runtime.GOARCH == "arm64" {
			arch = "arm64"
			triple = "aarch64-pc-windows-msvc"
		}
		root := filepath.Dir(path)
		for _, p := range []string{
			filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-"+arch, "vendor", triple, "bin", "codex.exe"),
			filepath.Join(root, "node_modules", "@openai", "codex", "vendor", triple, "codex", "codex.exe"),
		} {
			if info, e := os.Stat(p); e == nil && info.Mode().IsRegular() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("不能直接启动脚本包装器 %s；请通过 RELAY_CODEX_BIN 或 RELAY_CLAUDE_CODE_BIN 指定原生 .exe", path)
}
