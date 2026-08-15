//go:build linux

package runnerfiles

import (
	"errors"

	"golang.org/x/sys/unix"
)

// linuxRoot 是 workspace 根目录的 Linux fd 实现。
type linuxRoot struct {
	fd int
}

// openWorkspaceRoot 以 O_PATH|O_DIRECTORY 打开 workspace 根并探测
// openat2 可用性。
//
// 探测使用与业务一致的 RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS 对根自身
// 做一次 openat2；ENOSYS 或 EINVAL 说明内核不支持，必须显式失败而不是
// 退回不安全的字符串路径解析。
func openWorkspaceRoot(path string) (workspaceRoot, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, translateErrno(err)
	}
	probe, err := unix.Openat2(
		fd,
		".",
		&unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
		},
	)
	if err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return nil, ErrUnavailable
		}
		return nil, translateErrno(err)
	}
	unix.Close(probe)
	return &linuxRoot{fd: fd}, nil
}

func (r *linuxRoot) close() error {
	if r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	return err
}

// openBeneath 在根 fd 之下解析 path 并按 how 打开目标。
//
// RESOLVE_BENEATH 保证最终对象仍在 workspace 内；RESOLVE_NO_MAGICLINKS
// 拒绝 procfs 等 magic link。how 由调用方按用途显式给出（只读、O_PATH、
// 目录等），本函数不添加隐式语义。
func openBeneath(rootFD int, path string, how *unix.OpenHow) (int, error) {
	fd, err := unix.Openat2(rootFD, path, how)
	if err != nil {
		return -1, translateErrno(err)
	}
	return fd, nil
}

// openParent 把 mutation 路径拆为安全父目录 fd 与最终分段名。
//
// 父目录以 O_PATH|O_DIRECTORY 打开；"." 父由 dup 根 fd 得到。最终分段
// 不再解析，交给 mkdirat/unlinkat/renameat2 等以 no-follow 语义处理，
// 因此并发 rename 竞争最多让操作失败，不能让目标越界。
func openParent(rootFD int, path string) (int, string, error) {
	parent, base := splitParent(path)
	if base == "" {
		return -1, "", ErrInvalidPath
	}
	var parentFD int
	var err error
	if parent == "." {
		parentFD, err = unix.Dup(rootFD)
		if err != nil {
			return -1, "", translateErrno(err)
		}
	} else {
		parentFD, err = openBeneath(rootFD, parent, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err != nil {
			return -1, "", err
		}
	}
	return parentFD, base, nil
}

// splitParent 返回 path 的父路径与最终分段；根路径没有父。
func splitParent(path string) (string, string) {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' {
			parent := path[:index]
			if parent == "" {
				parent = "."
			}
			return parent, path[index+1:]
		}
	}
	return ".", path
}

// translateErrno 把 syscall errno 映射为本包稳定错误。
//
// 不明 errno 统一映射为 ErrConflict 之外的保守失败；调用方不得把原始
// errno 文本透传到公共响应。
func translateErrno(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return ErrNotFound
	case errors.Is(err, unix.EEXIST):
		return ErrConflict
	case errors.Is(err, unix.EISDIR), errors.Is(err, unix.ENOTDIR):
		return ErrTypeMismatch
	case errors.Is(err, unix.EFBIG), errors.Is(err, unix.ENOSPC):
		return ErrTooLarge
	default:
		return err
	}
}
