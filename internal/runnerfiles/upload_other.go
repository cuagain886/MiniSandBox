//go:build !linux

package runnerfiles

import (
	"io"

	"minisandbox/pkg/protocol"
)

// Upload 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Upload(string, io.Reader, bool, bool, int64) (bool, protocol.FileStat, error) {
	return false, protocol.FileStat{}, ErrUnavailable
}
