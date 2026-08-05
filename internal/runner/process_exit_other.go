//go:build !unix

package runner

func normalizedProcessExitCode(state processState) (int, bool) {
	if state == nil {
		return 0, false
	}
	exitCode := state.ExitCode()
	return exitCode, exitCode >= 0
}
