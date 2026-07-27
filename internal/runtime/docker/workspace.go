package docker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Workspace 描述宿主机目录与容器内挂载路径的对应关系。
type Workspace struct {
	HostPath      string
	ContainerPath string
}

// RuntimePaths 保存单个 sandbox 已准备好的宿主机通信路径。
type RuntimePaths struct {
	// Directory 是权限收敛为 0700 的 sandbox 独立 runtime directory。
	Directory string
	// HostRunnerSocket 是 runner 后续绑定的 Unix Socket 路径；本任务不创建它。
	HostRunnerSocket string
}

// EnsureRuntimeDirectory 幂等创建 `<dataDirectory>/run/<sandboxID>`。
//
// P1-012 必须先准备真实的 data/run 根目录；本函数拒绝 symlink、非目录占位
// 和越界 ID，只创建 sandbox 子目录，不创建 socket、workspace 或 Docker 资源。
func EnsureRuntimeDirectory(
	dataDirectory string,
	sandboxID string,
) (RuntimePaths, error) {
	names, err := NamesForSandbox(dataDirectory, sandboxID)
	if err != nil {
		return RuntimePaths{}, err
	}
	runRoot := filepath.Dir(names.RuntimeDirectory)
	if err := requireRealDirectory(runRoot); err != nil {
		return RuntimePaths{}, fmt.Errorf("validate runtime root: %w", err)
	}

	err = os.Mkdir(names.RuntimeDirectory, 0o700)
	switch {
	case err == nil:
		// 创建成功后仍通过 Lstat 校验，统一首次和重复调用的安全语义。
	case errors.Is(err, os.ErrExist):
	default:
		return RuntimePaths{}, fmt.Errorf("create runtime directory: %w", err)
	}
	if err := requireRealDirectory(names.RuntimeDirectory); err != nil {
		return RuntimePaths{}, fmt.Errorf("validate runtime directory: %w", err)
	}
	// umask 或先前进程可能留下更宽权限；每次幂等调用都重新收敛。
	if err := os.Chmod(names.RuntimeDirectory, 0o700); err != nil {
		return RuntimePaths{}, fmt.Errorf("restrict runtime directory: %w", err)
	}

	return RuntimePaths{
		Directory:        names.RuntimeDirectory,
		HostRunnerSocket: names.HostRunnerSocket,
	}, nil
}

// requireRealDirectory 要求路径存在、是目录且本身不是 symlink。
func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed path is a symlink")
	}
	if !info.IsDir() {
		return errors.New("managed path is not a directory")
	}
	return nil
}
