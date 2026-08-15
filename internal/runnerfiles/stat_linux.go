//go:build linux

package runnerfiles

import (
	"fmt"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"minisandbox/pkg/protocol"
)

// Stat 返回指定路径的 metadata；最终 symlink 被跟随，但解析结果保证
// 仍在 workspace 内。
func (s *Service) Stat(path string) (protocol.FileStat, error) {
	if err := validatePath(path); err != nil {
		return protocol.FileStat{}, err
	}
	fd, err := openBeneath(s.root.rootFD(), path, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return protocol.FileStat{}, err
	}
	defer unix.Close(fd)
	var raw unix.Stat_t
	if err := unix.Fstat(fd, &raw); err != nil {
		return protocol.FileStat{}, translateErrno(err)
	}
	return statFromRaw(path, &raw), nil
}

// List 返回目录直接子项；条目 metadata 使用 no-follow 语义，symlink
// 按其本体报告。目录并发变化时结果是尽力而为的快照。
func (s *Service) List(path string) (protocol.DirectoryListing, error) {
	if err := validatePath(path); err != nil {
		return protocol.DirectoryListing{}, err
	}
	dirFD, err := openBeneath(s.root.rootFD(), path, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return protocol.DirectoryListing{}, err
	}
	defer unix.Close(dirFD)
	// os.File 只借用 fd 负责枚举 dirents；metadata 一律用 Fstatat
	// no-follow 从同一 dirFD 读取，避免借助路径名的二次解析。
	directory := os.NewFile(uintptr(dirFD), "")
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return protocol.DirectoryListing{}, translateErrno(err)
	}
	sort.Strings(names)

	entries := make([]protocol.FileStat, 0, len(names))
	for _, name := range names {
		var raw unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &raw, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			// 并发删除的条目直接跳过，不中断整页结果。
			if err == unix.ENOENT {
				continue
			}
			return protocol.DirectoryListing{}, translateErrno(err)
		}
		entryPath := name
		if path != "." {
			entryPath = path + "/" + name
		}
		entries = append(entries, statFromRaw(entryPath, &raw))
	}
	return protocol.DirectoryListing{Path: path, Entries: entries}, nil
}

// statFromRaw 把 fstat 结果映射为公共 FileStat。
func statFromRaw(path string, raw *unix.Stat_t) protocol.FileStat {
	fileType := protocol.FileTypeOther
	switch raw.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		fileType = protocol.FileTypeRegular
	case unix.S_IFDIR:
		fileType = protocol.FileTypeDirectory
	case unix.S_IFLNK:
		fileType = protocol.FileTypeSymlink
	}
	size := raw.Size
	if fileType == protocol.FileTypeDirectory {
		size = 0
	}
	return protocol.FileStat{
		Path:       path,
		Type:       fileType,
		SizeBytes:  size,
		Mode:       fmt.Sprintf("%04o", raw.Mode&0o777),
		ModifiedAt: time.Unix(raw.Mtim.Sec, raw.Mtim.Nsec).UTC(),
	}
}
