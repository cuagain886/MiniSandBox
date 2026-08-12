//go:build linux

package runtime

import (
	"errors"
	"os"
	"syscall"
)

func readLeaseManifestNoFollow(path string, expected os.FileInfo) ([]byte, error) {
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), LeaseManifestName)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errors.New("open lease manifest")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(expected, actual) {
		return nil, errors.New("lease manifest changed during inspection")
	}
	return readLeaseManifestContent(file)
}

func leaseManifestOwnerSafe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func leaseManifestModeSafe(info os.FileInfo) bool {
	return info.Mode().Perm() == 0o600
}
