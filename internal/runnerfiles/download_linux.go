//go:build linux

package runnerfiles

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Download 打开一个 workspace 普通文件并返回流式 reader。
//
// 只接受 regular file；目录和其他类型返回 ErrTypeMismatch。文件超过
// limit 时立即失败而不是静默截断。读取过程中被并发截断只会产生读取
// 错误，不会越权访问其他对象。
func (s *Service) Download(path string, limit int64) (*Download, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, ErrTooLarge
	}
	fd, err := openBeneath(s.root.rootFD(), path, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	var raw unix.Stat_t
	if err := unix.Fstat(fd, &raw); err != nil {
		unix.Close(fd)
		return nil, translateErrno(err)
	}
	if raw.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, ErrTypeMismatch
	}
	if raw.Size > limit {
		unix.Close(fd)
		return nil, ErrTooLarge
	}
	file := os.NewFile(uintptr(fd), path)
	return &Download{
		Stat:   statFromRaw(path, &raw),
		Reader: &sectionCloser{section: io.NewSectionReader(file, 0, raw.Size), file: file},
	}, nil
}

// ensure io.ReadCloser 装配满足接口检查。
var _ io.ReadCloser = (*sectionCloser)(nil)
