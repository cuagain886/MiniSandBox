//go:build linux

package runnerfiles

import (
	"strings"

	"golang.org/x/sys/unix"

	"minisandbox/pkg/protocol"
)

// Mkdir 创建 workspace 目录；返回 created 表示本次调用是否真的新建。
//
// parents 为 true 时逐段创建缺失祖先并接受已存在的目录；为 false 时只在
// 已存在的父目录中创建单层。目录权限固定为 0755，符号链接不能被当作
// 目录穿越（每段打开都带 O_NOFOLLOW）。
func (s *Service) Mkdir(path string, parents bool) (bool, protocol.FileStat, error) {
	if path == "." {
		return false, protocol.FileStat{}, ErrInvalidPath
	}
	if err := validatePath(path); err != nil {
		return false, protocol.FileStat{}, err
	}

	if !parents {
		parentFD, base, err := openParent(s.root.rootFD(), path)
		if err != nil {
			return false, protocol.FileStat{}, err
		}
		defer unix.Close(parentFD)
		if err := unix.Mkdirat(parentFD, base, 0o755); err != nil {
			if err == unix.EEXIST {
				created, stat, statErr := statEntry(parentFD, base, path)
				if statErr != nil {
					return false, protocol.FileStat{}, statErr
				}
				if !created || stat.Type != protocol.FileTypeDirectory {
					return false, protocol.FileStat{}, ErrConflict
				}
				return false, stat, nil
			}
			return false, protocol.FileStat{}, translateErrno(err)
		}
		stat, err := statEntryAfterCreate(parentFD, base, path)
		if err != nil {
			return false, protocol.FileStat{}, err
		}
		return true, stat, nil
	}

	current, err := unix.Dup(s.root.rootFD())
	if err != nil {
		return false, protocol.FileStat{}, translateErrno(err)
	}
	created := false
	for _, segment := range strings.Split(path, "/") {
		next, segmentCreated, advanceErr := advanceDirectory(current, segment)
		if advanceErr != nil {
			unix.Close(current)
			return false, protocol.FileStat{}, advanceErr
		}
		unix.Close(current)
		current = next
		created = created || segmentCreated
	}
	var raw unix.Stat_t
	if err := unix.Fstat(current, &raw); err != nil {
		unix.Close(current)
		return false, protocol.FileStat{}, translateErrno(err)
	}
	unix.Close(current)
	return created, statFromRaw(path, &raw), nil
}

// advanceDirectory 在 current 目录中确保 segment 是目录并返回其 fd。
//
// 返回的 fd 由调用方负责关闭；segmentCreated 表示本次是否新建。
func advanceDirectory(current int, segment string) (int, bool, error) {
	if err := unix.Mkdirat(current, segment, 0o755); err == nil {
		child, openErr := openChildDirectory(current, segment)
		if openErr != nil {
			return -1, true, openErr
		}
		return child, true, nil
	} else if err != unix.EEXIST {
		return -1, false, translateErrno(err)
	}
	child, openErr := openChildDirectory(current, segment)
	if openErr != nil {
		return -1, false, openErr
	}
	return child, false, nil
}

// openChildDirectory 以 O_NOFOLLOW|O_DIRECTORY 打开子目录；symlink 或
// 非目录都会失败，不能被用来穿越。
func openChildDirectory(parent int, segment string) (int, error) {
	child, err := unix.Openat(
		parent,
		segment,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		if err == unix.ENOTDIR || err == unix.ELOOP {
			return -1, ErrTypeMismatch
		}
		return -1, translateErrno(err)
	}
	return child, nil
}

// statEntry 用 no-follow fstatat 读取条目；exists 为 false 表示并发消失。
func statEntry(parentFD int, base, path string) (bool, protocol.FileStat, error) {
	var raw unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &raw, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if err == unix.ENOENT {
			return false, protocol.FileStat{}, ErrNotFound
		}
		return false, protocol.FileStat{}, translateErrno(err)
	}
	return true, statFromRaw(path, &raw), nil
}

// statEntryAfterCreate 读取刚创建条目的 metadata。
func statEntryAfterCreate(parentFD int, base, path string) (protocol.FileStat, error) {
	_, stat, err := statEntry(parentFD, base, path)
	return stat, err
}
