//go:build linux

package runner

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"minisandbox/internal/runnerbootstrap"
)

const (
	runtimeDirectoryMode os.FileMode = 0o700
	runnerSocketMode     os.FileMode = 0o600
)

// BindManagedSocket 在 runner 降权前绑定固定 Unix Socket，并把路径权限收敛
// 为只有 sandboxd 数字 owner 可以连接。
//
// 本函数不接受请求路径；bootstrap 中任一路径偏离 P2-008 固定值都会失败。
func BindManagedSocket(bootstrap runnerbootstrap.Config) (net.Listener, error) {
	if bootstrap.Paths.RuntimeDirectory != runnerbootstrap.RuntimeDirectory ||
		bootstrap.Paths.SocketPath != runnerbootstrap.SocketPath ||
		filepath.Dir(bootstrap.Paths.SocketPath) != bootstrap.Paths.RuntimeDirectory {
		return nil, errors.New("runner socket bootstrap paths do not match fixed paths")
	}
	return bindManagedSocketAt(
		bootstrap.Paths.RuntimeDirectory,
		bootstrap.Paths.SocketPath,
		bootstrap.Identity,
	)
}

func bindManagedSocketAt(
	runtimeDirectory,
	socketPath string,
	identity runnerbootstrap.Identity,
) (listener net.Listener, err error) {
	if identity.ExecutionUID == identity.SocketOwnerUID ||
		identity.ExecutionGID == identity.SocketOwnerGID {
		return nil, errors.New("runner execution identity must differ from socket owner")
	}
	if filepath.Dir(socketPath) != runtimeDirectory {
		return nil, errors.New("runner socket must be a direct child of runtime directory")
	}
	if err := secureDirectory(
		runtimeDirectory,
		0,
		0,
		runtimeDirectoryMode,
	); err != nil {
		return nil, fmt.Errorf("secure runner runtime directory: %w", err)
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}

	listener, err = net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("bind runner socket: %w", err)
	}
	// 绑定后的任一 owner/mode 回验失败都必须撤销 pathname，不能留下看似可用
	// 但权限宽松的 socket 供后续重启误用。
	defer func() {
		if err != nil {
			_ = listener.Close()
			// 最后恢复目录 owner 后若回验失败，先用 CAP_CHOWN 重新取得目录，
			// 才能在没有 DAC_OVERRIDE 的 profile 下撤销 socket pathname。
			_ = os.Chown(runtimeDirectory, 0, 0)
			_ = os.Chmod(runtimeDirectory, runtimeDirectoryMode)
			_ = os.Remove(socketPath)
			_ = secureDirectory(runtimeDirectory, identity.SocketOwnerUID, identity.SocketOwnerGID, runtimeDirectoryMode)
			listener = nil
		}
	}()
	if err = os.Chmod(socketPath, runnerSocketMode); err != nil {
		return nil, fmt.Errorf("set runner socket mode: %w", err)
	}
	if err = os.Chown(socketPath, int(identity.SocketOwnerUID), int(identity.SocketOwnerGID)); err != nil {
		return nil, fmt.Errorf("set runner socket owner: %w", err)
	}
	if err = verifyManagedPath(socketPath, identity.SocketOwnerUID, identity.SocketOwnerGID, runnerSocketMode, os.ModeSocket); err != nil {
		return nil, fmt.Errorf("verify runner socket: %w", err)
	}
	// runtime 目录必须最后移交给 sandboxd；移交后 root runner 在无 DAC_OVERRIDE/FOWNER
	// 的最小 capability profile 下不再能按 pathname 访问目录，只继续持有 listener fd。
	if err = secureDirectory(runtimeDirectory, identity.SocketOwnerUID, identity.SocketOwnerGID, runtimeDirectoryMode); err != nil {
		return nil, fmt.Errorf("restore runner runtime directory owner: %w", err)
	}
	return listener, nil
}

func secureDirectory(path string, uid, gid uint32, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("runner runtime path must be a real directory")
	}
	// chmod 必须发生在移交 owner 之前；profile 没有 CAP_FOWNER，移交后再 chmod 会失败。
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if err := os.Chown(path, int(uid), int(gid)); err != nil {
		return err
	}
	return verifyManagedPath(path, uid, gid, mode, os.ModeDir)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return fmt.Errorf("inspect stale runner socket: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return errors.New("runner socket path is a symlink")
	case info.Mode()&os.ModeSocket == 0:
		return errors.New("runner socket path is occupied by a non-socket")
	default:
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale runner socket: %w", err)
		}
		return nil
	}
}

func verifyManagedPath(
	path string,
	uid,
	gid uint32,
	mode,
	kind os.FileMode,
) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != kind ||
		info.Mode().Perm() != mode.Perm() || stat.Uid != uid || stat.Gid != gid {
		return errors.New("managed socket path owner, mode, or type verification failed")
	}
	return nil
}
