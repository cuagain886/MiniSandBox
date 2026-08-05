//go:build linux

package runnerauth

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LoadMasterKey 从绝对 secret file 安全读取恰好 32 字节的主密钥。
//
// 文件必须是非 symlink regular file，权限不得宽于 0600。错误使用固定文案，
// 不回显路径、文件内容或底层可能包含路径的错误。
func LoadMasterKey(path string) (key MasterKey, err error) {
	if !filepath.IsAbs(path) {
		return MasterKey{}, errors.New("runner master key path must be absolute")
	}
	fd, openErr := syscall.Open(
		path,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if openErr != nil {
		return MasterKey{}, errors.New("open runner master key failed")
	}
	file := os.NewFile(uintptr(fd), "runner-master-key")
	if file == nil {
		_ = syscall.Close(fd)
		return MasterKey{}, errors.New("open runner master key failed")
	}
	defer file.Close()
	defer func() {
		if err != nil {
			key.Clear()
		}
	}()

	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&^os.FileMode(0o600) != 0 {
		return MasterKey{}, errors.New("runner master key file is unsafe")
	}
	if _, readErr := io.ReadFull(file, key[:]); readErr != nil {
		return MasterKey{}, errors.New("runner master key must contain exactly 32 bytes")
	}
	var extra [1]byte
	count, readErr := file.Read(extra[:])
	clear(extra[:])
	if count != 0 || readErr != io.EOF {
		return MasterKey{}, errors.New("runner master key must contain exactly 32 bytes")
	}
	if allZero(key[:]) {
		return MasterKey{}, errors.New("runner master key must not be an all-zero value")
	}
	return key, nil
}
