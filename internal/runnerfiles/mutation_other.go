//go:build !linux

package runnerfiles

import "minisandbox/pkg/protocol"

// Move 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Move(string, string, bool) (protocol.FileStat, error) {
	return protocol.FileStat{}, ErrUnavailable
}

// Delete 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Delete(string, bool) error { return ErrUnavailable }
