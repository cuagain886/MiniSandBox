//go:build !linux

package runner

func startCommand(spec CommandSpec) (StartedProcess, error) {
	spec.Close()
	return StartedProcess{}, ErrProcessStartFailed
}
