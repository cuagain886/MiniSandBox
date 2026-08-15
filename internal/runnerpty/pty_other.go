//go:build !linux

package runnerpty

// spawnPTYProcess 在不支持 PTY 的平台上显式失败。
func spawnPTYProcess(StartOptions) (ptyProcess, error) {
	return nil, ErrUnsupported
}
