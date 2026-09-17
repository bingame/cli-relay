package process

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

func manage(cmd *exec.Cmd) (func(), error) {
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	handle, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	defer windows.CloseHandle(handle)
	if e = windows.AssignProcessToJobObject(job, handle); e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	return func() { _ = windows.CloseHandle(job) }, nil
}

func configure(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if e := kill.Run(); e != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259
}

// Windows 没有 execve；直接继承控制台并等待，保持原生退出码。
func Replace(o Options) error {
	binary, err := ResolveBinary(o.Inputs.Binary)
	if err != nil {
		return err
	}
	cmd := exec.Command(binary, o.Inputs.Args...)
	cmd.Dir = o.Dir
	cmd.Env = Environment(os.Environ(), o.Inputs.UnsetEnv, o.Inputs.Env)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if e := cmd.Start(); e != nil {
		return e
	}
	cleanup, e := track(o, cmd.Process.Pid, "run", time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return e
	}
	defer cleanup()
	return cmd.Wait()
}
