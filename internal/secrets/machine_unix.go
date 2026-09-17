//go:build !windows

package secrets

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
)

func nativeMachineID() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("使用系统 machine-id 文件")
	}
	b, e := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if e != nil {
		return "", e
	}
	m := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`).FindSubmatch(b)
	if len(m) != 2 {
		return "", fmt.Errorf("无法读取平台UUID")
	}
	return string(m[1]), nil
}
