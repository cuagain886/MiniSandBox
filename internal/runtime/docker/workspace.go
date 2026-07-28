package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
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

// WorkspaceVolumeResult 描述 workspace volume 的幂等保证结果。
type WorkspaceVolumeResult struct {
	// Name 是 sandbox ID 对应的确定性 Docker named volume 名称。
	Name string
	// CreatedByThisCall 表示本次调用走过创建分支；复用已有 volume 时为 false。
	CreatedByThisCall bool
}

// ensureWorkspaceVolume 幂等保证 sandbox 的受管 workspace volume 存在。
//
// 已有同名 volume 必须携带匹配 sandbox ID、spec hash 和 schema 的完整 labels；
// 这样不会把用户创建的同名 volume 或其他规格遗留的资源误接管。
func ensureWorkspaceVolume(
	ctx context.Context,
	engine Engine,
	sandboxID string,
	specHash string,
) (WorkspaceVolumeResult, error) {
	name := workspaceName(sandboxID)
	expected := ManagedLabels{
		SandboxID: sandboxID,
		SpecHash:  specHash,
		Workspace: name,
	}
	labels, err := EncodeLabels(expected)
	if err != nil {
		return WorkspaceVolumeResult{}, fmt.Errorf("prepare workspace volume labels: %w", err)
	}

	inspection, err := engine.VolumeInspect(
		ctx,
		name,
		mobyclient.VolumeInspectOptions{},
	)
	switch {
	case err == nil:
		if err := validateWorkspaceVolume(inspection.Volume, name, expected); err != nil {
			return WorkspaceVolumeResult{}, err
		}
		return WorkspaceVolumeResult{Name: name}, nil
	case !errdefs.IsNotFound(err):
		return WorkspaceVolumeResult{}, &RuntimeUnavailableError{cause: err}
	}

	created, err := engine.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Name:   name,
		Labels: labels,
	})
	if err != nil {
		return WorkspaceVolumeResult{}, &RuntimeUnavailableError{cause: err}
	}
	// Docker 的命名资源创建可能与并发请求相遇，因此即使 Create 成功也要验证
	// daemon 返回的身份，不能仅凭请求参数假定拿到的是本 sandbox 的资源。
	if err := validateWorkspaceVolume(created.Volume, name, expected); err != nil {
		return WorkspaceVolumeResult{}, err
	}
	return WorkspaceVolumeResult{Name: name, CreatedByThisCall: true}, nil
}

// validateWorkspaceVolume 校验已有或新建 volume 的受管身份。
func validateWorkspaceVolume(
	volume mobyvolume.Volume,
	expectedName string,
	expected ManagedLabels,
) error {
	if volume.Name != expectedName {
		return workspaceVolumeConflict()
	}
	actual, err := ParseLabels(volume.Labels)
	if err != nil || actual != expected {
		return workspaceVolumeConflict()
	}
	return nil
}

// workspaceVolumeConflict 返回不泄露实际 labels 或资源元数据的固定冲突错误。
func workspaceVolumeConflict() error {
	return fmt.Errorf("workspace volume conflicts with managed identity: %w", domain.ErrConflict)
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
