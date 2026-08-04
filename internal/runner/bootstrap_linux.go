//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerbootstrap"
)

const managedDirectoryMode os.FileMode = 0o700

type managedDirectoryPaths struct {
	executionData string
	workspace     string
}

type directoryOps struct {
	lstat func(string) (os.FileInfo, error)
	mkdir func(string, os.FileMode) error
	chmod func(string, os.FileMode) error
	chown func(string, int, int) error
}

var osDirectoryOps = directoryOps{
	lstat: os.Lstat,
	mkdir: os.Mkdir,
	chmod: os.Chmod,
	chown: os.Chown,
}

// InitializeManagedDirectories 在 runner 仍为 root 的 bootstrap 阶段初始化
// execution data 目录与 workspace mount root。
//
// 路径必须精确来自内部协议固定值，不能由 execution 请求指定。函数只修改
// 两个目录根自身的 owner/mode，不递归触碰 workspace 中的用户文件。
func InitializeManagedDirectories(bootstrap runnerbootstrap.Config) error {
	if bootstrap.Paths.ExecutionDataDirectory != runnerbootstrap.ExecutionDataDirectory ||
		bootstrap.Paths.WorkspaceDirectory != domain.WorkspaceMountPath ||
		bootstrap.Paths.RuntimeDirectory != runnerbootstrap.RuntimeDirectory ||
		bootstrap.Paths.SocketPath != runnerbootstrap.SocketPath {
		return errors.New("runner bootstrap managed paths do not match fixed paths")
	}
	if bootstrap.Identity.ExecutionUID == 0 || bootstrap.Identity.ExecutionGID == 0 {
		return errors.New("runner execution identity must be non-root")
	}
	return initializeManagedDirectories(
		managedDirectoryPaths{
			executionData: runnerbootstrap.ExecutionDataDirectory,
			workspace:     bootstrap.Paths.WorkspaceDirectory,
		},
		bootstrap.Identity.ExecutionUID,
		bootstrap.Identity.ExecutionGID,
		osDirectoryOps,
	)
}

func initializeManagedDirectories(
	paths managedDirectoryPaths,
	uid,
	gid uint32,
	ops directoryOps,
) error {
	if err := requireRealDirectory(filepath.Dir(paths.executionData), ops); err != nil {
		return fmt.Errorf("validate runner runtime directory: %w", err)
	}
	if err := ensureManagedDirectory(paths.executionData, true, uid, gid, ops); err != nil {
		return fmt.Errorf("initialize runner execution data directory: %w", err)
	}
	// workspace 是 Docker 提供的 mount root；缺失时不能在镜像层临时创建，
	// 否则会掩盖挂载配置错误并把用户文件写入不可持久的容器层。
	if err := ensureManagedDirectory(paths.workspace, false, uid, gid, ops); err != nil {
		return fmt.Errorf("initialize runner workspace mount root: %w", err)
	}
	return nil
}

func requireRealDirectory(path string, ops directoryOps) error {
	info, err := ops.lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed parent must be a real directory")
	}
	return nil
}

func ensureManagedDirectory(
	path string,
	create bool,
	uid,
	gid uint32,
	ops directoryOps,
) error {
	info, err := ops.lstat(path)
	if os.IsNotExist(err) && create {
		if err := ops.mkdir(path, managedDirectoryMode); err != nil {
			return err
		}
		info, err = ops.lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed path must be a real directory")
	}
	if err := ops.chown(path, int(uid), int(gid)); err != nil {
		return err
	}
	if err := ops.chmod(path, managedDirectoryMode); err != nil {
		return err
	}

	verified, err := ops.lstat(path)
	if err != nil {
		return err
	}
	stat, ok := verified.Sys().(*syscall.Stat_t)
	if !ok || verified.Mode()&os.ModeSymlink != 0 || !verified.IsDir() ||
		verified.Mode().Perm() != managedDirectoryMode.Perm() ||
		stat.Uid != uid || stat.Gid != gid {
		return errors.New("managed directory owner or mode verification failed")
	}
	return nil
}
