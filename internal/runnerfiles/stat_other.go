//go:build !linux

package runnerfiles

import "minisandbox/pkg/protocol"

// Stat 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Stat(string) (protocol.FileStat, error) {
	return protocol.FileStat{}, ErrUnavailable
}

// List 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) List(string) (protocol.DirectoryListing, error) {
	return protocol.DirectoryListing{}, ErrUnavailable
}
