//go:build !linux

package runnerfiles

// unsupportedRoot 在缺少 openat2 等 fd-relative syscall 的平台上显式失败。
type unsupportedRoot struct{}

func openWorkspaceRoot(string) (workspaceRoot, error) {
	return nil, ErrUnavailable
}

func (unsupportedRoot) close() error  { return nil }
func (unsupportedRoot) rootFD() int  { return -1 }
