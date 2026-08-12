//go:build !linux

package runtime

import (
	"errors"
	"os"
)

func readLeaseManifestNoFollow(path string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(expected, actual) {
		return nil, errors.New("lease manifest changed during inspection")
	}
	return readLeaseManifestContent(file)
}

func leaseManifestOwnerSafe(os.FileInfo) bool {
	// Windows 的 ACL 所有权不由 os.FileInfo 暴露；受管根 ACL 在 datadir 初始化阶段校验。
	return true
}

func leaseManifestModeSafe(os.FileInfo) bool {
	// Windows 权限由受管根 ACL 表达，os.FileMode.Perm 不具有 POSIX 0600 语义。
	return true
}
