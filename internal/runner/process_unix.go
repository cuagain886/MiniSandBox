//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup 让用户命令成为独立进程组组长。
//
// 独立进程组使 timeout 和 cancel 能同时终止 shell 及其派生子进程，避免后台
// 进程在执行结束后继续占用 sandbox 资源。
func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalProcessGroup 向 pid 所代表的整个进程组发送信号。
func signalProcessGroup(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}
