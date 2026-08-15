//go:build linux

package runnerfiles

import (
	"crypto/rand"
	"encoding/hex"
	"io"

	"golang.org/x/sys/unix"

	"minisandbox/pkg/protocol"
)

// tempPrefix 是上传临时文件在目标目录内的固定前缀；随机后缀保证不可预测。
const tempPrefix = ".minisandbox-upload-"

// Upload 把 reader 的内容原子写入 path。
//
// 流程：在目标父目录创建随机临时文件（O_EXCL|O_NOFOLLOW）→ 有界流式
// 拷贝 → fsync → chmod 0644 → renameat2 发布。overwrite 为 false 时使用
// RENAME_NOREPLACE，已存在目标保持不变；任何失败路径都删除临时文件，
// 目标在成功前不可见。createParents 为 true 时先逐段创建缺失父目录。
// limit 是本次上传的最大字节数，超限返回 ErrTooLarge。
func (s *Service) Upload(
	path string,
	reader io.Reader,
	overwrite bool,
	createParents bool,
	limit int64,
) (bool, protocol.FileStat, error) {
	if path == "." {
		return false, protocol.FileStat{}, ErrInvalidPath
	}
	if err := validatePath(path); err != nil {
		return false, protocol.FileStat{}, err
	}
	if limit <= 0 {
		return false, protocol.FileStat{}, ErrTooLarge
	}
	if createParents {
		parent, _ := splitParent(path)
		if parent != "." {
			if _, _, err := s.Mkdir(parent, true); err != nil {
				return false, protocol.FileStat{}, err
			}
		}
	}
	parentFD, base, err := openParent(s.root.rootFD(), path)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	defer unix.Close(parentFD)

	// 状态分类用预检：已存在且不允许覆盖时直接失败；该检查与 rename 之间
	// 的竞争最多影响 200/201 的标注，不影响内容正确性。
	preExists, _, statErr := statEntry(parentFD, base, path)
	if statErr != nil && statErr != ErrNotFound {
		return false, protocol.FileStat{}, statErr
	}
	if preExists && !overwrite {
		return false, protocol.FileStat{}, ErrConflict
	}

	tempFD, tempName, err := createTempFile(parentFD)
	if err != nil {
		return false, protocol.FileStat{}, err
	}
	replaced := preExists
	committed := false
	defer func() {
		if !committed {
			unix.Unlinkat(parentFD, tempName, 0)
		}
	}()

	written, copyErr := copyBounded(tempFD, reader, limit)
	if copyErr != nil {
		return false, protocol.FileStat{}, copyErr
	}
	_ = written
	if err := unix.Fsync(tempFD); err != nil {
		return false, protocol.FileStat{}, translateErrno(err)
	}
	if err := unix.Fchmod(tempFD, 0o644); err != nil {
		return false, protocol.FileStat{}, translateErrno(err)
	}

	flags := uint(0)
	if !overwrite {
		flags = unix.RENAME_NOREPLACE
	}
	if err := unix.Renameat2(parentFD, tempName, parentFD, base, flags); err != nil {
		if err == unix.EEXIST {
			return false, protocol.FileStat{}, ErrConflict
		}
		return false, protocol.FileStat{}, translateErrno(err)
	}
	committed = true

	exists, stat, statErr := statEntry(parentFD, base, path)
	if statErr != nil || !exists {
		return replaced, protocol.FileStat{}, statErr
	}
	return replaced, stat, nil
}

// createTempFile 在 parentFD 中创建不可预测的独占临时文件。
func createTempFile(parentFD int) (int, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return -1, "", err
		}
		name := tempPrefix + hex.EncodeToString(random[:])
		fd, err := unix.Openat(
			parentFD,
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0o600,
		)
		if err == nil {
			return fd, name, nil
		}
		if err != unix.EEXIST {
			return -1, "", translateErrno(err)
		}
	}
	return -1, "", ErrConflict
}

// copyBounded 把 reader 流式拷贝到 fd，写入超过 limit 时失败。
func copyBounded(fd int, reader io.Reader, limit int64) (int64, error) {
	var total int64
	buffer := make([]byte, 32*1024)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			total += int64(read)
			if total > limit {
				return total, ErrTooLarge
			}
			written := 0
			for written < read {
				chunk, writeErr := unix.Write(fd, buffer[written:read])
				if chunk <= 0 || writeErr != nil {
					return total, translateErrno(writeErr)
				}
				written += chunk
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
