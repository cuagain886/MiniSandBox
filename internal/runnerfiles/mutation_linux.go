//go:build linux

package runnerfiles

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"minisandbox/pkg/protocol"
)

// 递归删除的有界预算，防止超大目录把 runner 卡死。
const (
	maxRecursiveEntries = 10000
	maxRecursiveDepth   = 64
)

// Move 在同一 workspace 内移动（重命名）路径。
//
// 源与目的都以安全父目录 fd + 最终分段表达；renameat2 保证原子性。
// overwrite 为 false 时使用 RENAME_NOREPLACE；为 true 时拒绝把目录替换
// 到不同类型目标（内核按 EISDIR/ENOTDIR 拒绝）。根目录不能参与移动。
func (s *Service) Move(source, destination string, overwrite bool) (protocol.FileStat, error) {
	if source == "." || destination == "." {
		return protocol.FileStat{}, ErrInvalidPath
	}
	if err := validatePath(source); err != nil {
		return protocol.FileStat{}, err
	}
	if err := validatePath(destination); err != nil {
		return protocol.FileStat{}, err
	}
	sourceParent, sourceBase, err := openParent(s.root.rootFD(), source)
	if err != nil {
		return protocol.FileStat{}, err
	}
	defer unix.Close(sourceParent)
	destinationParent, destinationBase, err := openParent(s.root.rootFD(), destination)
	if err != nil {
		return protocol.FileStat{}, err
	}
	defer unix.Close(destinationParent)

	flags := uint(0)
	if !overwrite {
		flags = unix.RENAME_NOREPLACE
	}
	if err := unix.Renameat2(
		sourceParent,
		sourceBase,
		destinationParent,
		destinationBase,
		flags,
	); err != nil {
		if err == unix.EEXIST {
			return protocol.FileStat{}, ErrConflict
		}
		return protocol.FileStat{}, translateErrno(err)
	}
	exists, stat, statErr := statEntry(destinationParent, destinationBase, destination)
	if statErr != nil || !exists {
		return protocol.FileStat{}, statErr
	}
	return stat, nil
}

// Delete 删除文件、symlink 或目录。
//
// 目录在 recursive 为 false 时必须为空；递归删除不跟随符号链接并受
// 条目数与深度预算约束。目标不存在时幂等成功；workspace 根不能删除。
func (s *Service) Delete(path string, recursive bool) error {
	if path == "." {
		return ErrInvalidPath
	}
	if err := validatePath(path); err != nil {
		return err
	}
	parentFD, base, err := openParent(s.root.rootFD(), path)
	if errors.Is(err, ErrNotFound) {
		// 父目录已不存在时目标必然不存在；删除保持幂等成功。
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)

	var raw unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &raw, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return translateErrno(err)
	}
	if raw.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err := unix.Unlinkat(parentFD, base, 0); err != nil {
			return translateErrno(err)
		}
		return nil
	}
	if !recursive {
		if err := unix.Unlinkat(parentFD, base, unix.AT_REMOVEDIR); err != nil {
			if err == unix.ENOTEMPTY {
				return ErrConflict
			}
			return translateErrno(err)
		}
		return nil
	}
	budget := &deleteBudget{}
	dirFD, err := unix.Openat(
		parentFD,
		base,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return translateErrno(err)
	}
	if err := removeTree(dirFD, budget, 0); err != nil {
		unix.Close(dirFD)
		return err
	}
	unix.Close(dirFD)
	if err := unix.Unlinkat(parentFD, base, unix.AT_REMOVEDIR); err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return translateErrno(err)
	}
	return nil
}

// deleteBudget 跟踪递归删除的条目与深度消耗。
type deleteBudget struct{ entries int }

// removeTree 深度优先删除 dirFD 目录树中的全部内容；不跟随符号链接。
func removeTree(dirFD int, budget *deleteBudget, depth int) error {
	if depth > maxRecursiveDepth {
		return ErrConflict
	}
	dirents, err := readDirentNames(dirFD)
	if err != nil {
		return err
	}
	for _, name := range dirents {
		budget.entries++
		if budget.entries > maxRecursiveEntries {
			return ErrConflict
		}
		var raw unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &raw, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if err == unix.ENOENT {
				continue
			}
			return translateErrno(err)
		}
		if raw.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(dirFD, name, 0); err != nil && err != unix.ENOENT {
				return translateErrno(err)
			}
			continue
		}
		child, err := unix.Openat(
			dirFD,
			name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if err != nil {
			return translateErrno(err)
		}
		childErr := removeTree(child, budget, depth+1)
		unix.Close(child)
		if childErr != nil {
			return childErr
		}
		if err := unix.Unlinkat(dirFD, name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
			return translateErrno(err)
		}
	}
	return nil
}

// readDirentNames 通过 os.File 枚举目录 fd 的条目名。
//
// os.File 只借用 fd 做枚举，不调用 Close；fd 的生命周期由调用方管理。
func readDirentNames(dirFD int) ([]string, error) {
	directory := os.NewFile(uintptr(dirFD), "")
	names, err := directory.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, translateErrno(err)
	}
	return names, nil
}
