//go:build unix

package runner

import "syscall"

func normalizedProcessExitCode(state processState) (int, bool) {
	if state == nil {
		return 0, false
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		switch {
		case status.Exited():
			return status.ExitStatus(), true
		case status.Signaled():
			return 128 + int(status.Signal()), true
		default:
			return 0, false
		}
	}
	exitCode := state.ExitCode()
	return exitCode, exitCode >= 0
}
