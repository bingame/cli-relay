//go:build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
func manage(cmd *exec.Cmd) (func(), error) {
	return func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}, nil
}
func Alive(pid int) bool {
	return pid > 0 && (syscall.Kill(pid, 0) == nil || syscall.Kill(pid, 0) == syscall.EPERM)
}
func Replace(o Options) error {
	binary, e := ResolveBinary(o.Inputs.Binary)
	if e != nil {
		return e
	}
	if o.Dir != "" {
		if e = os.Chdir(o.Dir); e != nil {
			return e
		}
	}
	cleanup, e := track(o, os.Getpid(), "run", time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return e
	}
	e = syscall.Exec(binary, append([]string{binary}, o.Inputs.Args...), Environment(os.Environ(), o.Inputs.UnsetEnv, o.Inputs.Env))
	cleanup()
	return fmt.Errorf("替换进程失败: %w", e)
}
