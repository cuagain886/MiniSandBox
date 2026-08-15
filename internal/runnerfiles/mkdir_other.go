//go:build !linux

package runnerfiles

import "minisandbox/pkg/protocol"

// Mkdir 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Mkdir(string, bool) (bool, protocol.FileStat, error) {
	return false, protocol.FileStat{}, ErrUnavailable
}
