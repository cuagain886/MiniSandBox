//go:build linux

package runner

import (
	"os/exec"
	"syscall"
)

func configureParentDeathSafety(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
