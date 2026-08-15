//go:build !linux

package runnerfiles

// Download 在不支持 fd-relative syscall 的平台上显式失败。
func (s *Service) Download(string, int64) (*Download, error) {
	return nil, ErrUnavailable
}
