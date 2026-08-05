//go:build linux

package runnerauth

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const credentialFileMode os.FileMode = 0o600

// StageTokenFile 把派生 token 通过受管 runtime 目录中的固定一次性文件暂存。
//
// token 无论成功失败都会被清零。临时文件由 CreateTemp 以 O_EXCL 创建，完成
// owner/mode 回验与 fsync 后在同目录原子 rename；不会跟随 runtime/target symlink。
func StageTokenFile(
	runtimeDirectory string,
	ownerUID,
	ownerGID uint32,
	token *Token,
) (err error) {
	if token == nil {
		return errors.New("runner token is required")
	}
	defer token.Clear()
	if allZero(token[:]) {
		return errors.New("runner token is invalid")
	}
	if err := verifyCredentialDirectory(runtimeDirectory, ownerUID, ownerGID); err != nil {
		return err
	}
	target := filepath.Join(runtimeDirectory, CredentialFileName)
	if err := validateReplaceableCredential(target, ownerUID, ownerGID); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(runtimeDirectory, ".runner-token.tmp-")
	if err != nil {
		return errors.New("create runner credential file failed")
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chown(int(ownerUID), int(ownerGID)); err != nil {
		return errors.New("set runner credential owner failed")
	}
	if err := temporary.Chmod(credentialFileMode); err != nil {
		return errors.New("set runner credential mode failed")
	}
	if err := writeFull(temporary, token[:]); err != nil {
		return errors.New("write runner credential failed")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync runner credential failed")
	}
	if err := verifyOpenCredential(temporary, ownerUID, ownerGID); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close runner credential failed")
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return errors.New("publish runner credential failed")
	}
	if err := syncCredentialDirectory(runtimeDirectory); err != nil {
		return err
	}
	return verifyCredentialPath(target, ownerUID, ownerGID)
}

// ConsumeTokenFile 以 no-follow 方式读取固定凭据文件，回验路径与打开 fd 指向
// 同一 regular file 后立即 unlink。成功返回的 Token 由调用方在配置 HTTP auth
// 后负责 Clear；失败路径自动清零读取缓冲区。
func ConsumeTokenFile(
	runtimeDirectory string,
	ownerUID,
	ownerGID uint32,
) (token Token, err error) {
	defer func() {
		if err != nil {
			token.Clear()
		}
	}()
	if err := verifyCredentialDirectory(runtimeDirectory, ownerUID, ownerGID); err != nil {
		return Token{}, err
	}
	path := filepath.Join(runtimeDirectory, CredentialFileName)
	fd, openErr := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if openErr != nil {
		return Token{}, errors.New("open runner credential failed")
	}
	file := os.NewFile(uintptr(fd), "runner-token")
	if file == nil {
		_ = syscall.Close(fd)
		return Token{}, errors.New("open runner credential failed")
	}
	defer file.Close()
	if err := verifyOpenCredential(file, ownerUID, ownerGID); err != nil {
		return Token{}, err
	}
	if _, err := io.ReadFull(file, token[:]); err != nil {
		return Token{}, errors.New("runner credential length is invalid")
	}
	var extra [1]byte
	count, readErr := file.Read(extra[:])
	clear(extra[:])
	if count != 0 || readErr != io.EOF || allZero(token[:]) {
		return Token{}, errors.New("runner credential length or value is invalid")
	}
	if err := verifyCredentialPathMatchesFile(path, file, ownerUID, ownerGID); err != nil {
		return Token{}, err
	}
	if err := os.Remove(path); err != nil {
		return Token{}, errors.New("remove consumed runner credential failed")
	}
	if err := syncCredentialDirectory(runtimeDirectory); err != nil {
		return Token{}, err
	}
	return token, nil
}

func verifyCredentialDirectory(path string, uid, gid uint32) error {
	if !filepath.IsAbs(path) {
		return errors.New("runner credential directory must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("runner credential directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid {
		return errors.New("runner credential directory owner is invalid")
	}
	return nil
}

func validateReplaceableCredential(path string, uid, gid uint32) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != credentialFileMode {
		return errors.New("existing runner credential is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid {
		return errors.New("existing runner credential owner is invalid")
	}
	return nil
}

func verifyOpenCredential(file *os.File, uid, gid uint32) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != credentialFileMode || info.Size() != tokenBytes {
		return errors.New("runner credential file is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid {
		return errors.New("runner credential file owner is invalid")
	}
	return nil
}

func verifyCredentialPath(path string, uid, gid uint32) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != credentialFileMode || info.Size() != tokenBytes {
		return errors.New("published runner credential is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid {
		return errors.New("published runner credential owner is invalid")
	}
	return nil
}

func verifyCredentialPathMatchesFile(path string, file *os.File, uid, gid uint32) error {
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("runner credential path changed during consumption")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return errors.New("runner credential file changed during consumption")
	}
	pathStat, pathOK := pathInfo.Sys().(*syscall.Stat_t)
	fileStat, fileOK := fileInfo.Sys().(*syscall.Stat_t)
	if !pathOK || !fileOK || pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino || pathStat.Uid != uid || pathStat.Gid != gid {
		return errors.New("runner credential path identity changed during consumption")
	}
	return nil
}

func writeFull(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func syncCredentialDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open runner credential directory failed")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync runner credential directory failed")
	}
	return nil
}
