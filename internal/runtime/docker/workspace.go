package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
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
	// CreatedByThisCall 表示本次调用创建了 runtime directory。
	//
	// 调用方只能用它补偿同一次操作产生的副作用，不能据此删除复用目录。
	CreatedByThisCall bool
}

// WorkspaceVolumeResult 描述 workspace volume 的幂等保证结果。
type WorkspaceVolumeResult struct {
	// Name 是 sandbox ID 对应的确定性 Docker named volume 名称。
	Name string
	// CreatedByThisCall 表示本次调用走过创建分支；复用已有 volume 时为 false。
	CreatedByThisCall bool
}

// CleanupPendingError 表示受管资源身份已确认，但仍被其他 runtime 资源占用。
//
// 删除编排可在后续 reconcile 重试；固定错误文本不回显 Docker 引用关系。
type CleanupPendingError struct {
	cause        error
	operationErr error
}

// Error 返回安全、稳定的 cleanup pending 文案。
func (*CleanupPendingError) Error() string {
	return "sandbox runtime cleanup is pending"
}

// Unwrap 返回 Docker cause，供内部 errors.Is/As 诊断。
func (e *CleanupPendingError) Unwrap() error {
	return errors.Join(e.operationErr, e.cause)
}

// CleanupPending 标记该错误需要后续清理重试。
func (*CleanupPendingError) CleanupPending() bool {
	return true
}

// FailureReason 返回稳定的 cleanup pending 生命周期 reason。
func (*CleanupPendingError) FailureReason() string {
	return runtimeport.FailureReasonCleanupPending
}

// OperationError 返回补偿开始前的原始创建失败。
//
// 普通删除错误没有独立 operation error，返回 nil；reconciler 只在后续幂等
// Delete 已成功时使用该值恢复原始失败分类。
func (e *CleanupPendingError) OperationError() error {
	return e.operationErr
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
	creationExpiry ...time.Time,
) (WorkspaceVolumeResult, error) {
	name := workspaceName(sandboxID)
	expected := ManagedLabels{
		SandboxID:             sandboxID,
		SpecHash:              specHash,
		Workspace:             name,
		RunnerProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
	}
	if len(creationExpiry) > 0 {
		expiresAt := creationExpiry[0].UTC()
		expected.ExpiresAt = &expiresAt
	}
	labels, err := EncodeLabels(expected)
	if err != nil {
		return WorkspaceVolumeResult{}, fmt.Errorf("prepare workspace volume labels: %w", err)
	}
	// 新建卷显式声明资源职责；reader 仍接受没有该扩展 label 的旧卷，避免升级时误报。
	labels[LabelResourceRole] = resourceRoleWorkspace

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
	result := WorkspaceVolumeResult{Name: name, CreatedByThisCall: true}
	// Docker 的命名资源创建可能与并发请求相遇，因此即使 Create 成功也要验证
	// daemon 返回的身份，不能仅凭请求参数假定拿到的是本 sandbox 的资源。
	if err := validateWorkspaceVolume(created.Volume, name, expected); err != nil {
		return result, err
	}
	return result, nil
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
	if err != nil || !managedLabelIdentityEqual(actual, expected) {
		return workspaceVolumeConflict()
	}
	return nil
}

// workspaceVolumeConflict 返回不泄露实际 labels 或资源元数据的固定冲突错误。
func workspaceVolumeConflict() error {
	return fmt.Errorf("workspace volume conflicts with managed identity: %w", domain.ErrConflict)
}

// deleteWorkspaceVolume 幂等删除当前 sandbox 的非持久受管 volume。
//
// 只使用 Force=false；volume in use 不升级为强制删除，而是返回 cleanup
// pending，等待容器清理完成后由后续 reconcile 重试。
func deleteWorkspaceVolume(
	ctx context.Context,
	engine Engine,
	sandboxID string,
) error {
	if !validSandboxID(sandboxID) {
		return errors.New("sandbox ID is invalid")
	}
	name := workspaceName(sandboxID)
	inspection, err := engine.VolumeInspect(
		ctx,
		name,
		mobyclient.VolumeInspectOptions{},
	)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return runtimeUnavailable(err)
	}
	metadata, parseErr := ParseLabels(inspection.Volume.Labels)
	if inspection.Volume.Name != name ||
		parseErr != nil ||
		metadata.SandboxID != sandboxID ||
		metadata.Workspace != name {
		return workspaceVolumeConflict()
	}

	_, err = engine.VolumeRemove(
		ctx,
		name,
		mobyclient.VolumeRemoveOptions{Force: false},
	)
	if err == nil || errdefs.IsNotFound(err) {
		return nil
	}
	if errdefs.IsConflict(err) || errdefs.IsFailedPrecondition(err) {
		return &CleanupPendingError{cause: err}
	}
	return runtimeUnavailable(err)
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

	paths := RuntimePaths{
		Directory:        names.RuntimeDirectory,
		HostRunnerSocket: names.HostRunnerSocket,
	}
	err = os.Mkdir(names.RuntimeDirectory, 0o700)
	switch {
	case err == nil:
		paths.CreatedByThisCall = true
		// 创建成功后仍通过 Lstat 校验，统一首次和重复调用的安全语义。
	case errors.Is(err, os.ErrExist):
	default:
		return RuntimePaths{}, fmt.Errorf("create runtime directory: %w", err)
	}
	if err := requireRealDirectory(names.RuntimeDirectory); err != nil {
		return paths, fmt.Errorf("validate runtime directory: %w", err)
	}
	// umask 或先前进程可能留下更宽权限；每次幂等调用都重新收敛。
	if err := os.Chmod(names.RuntimeDirectory, 0o700); err != nil {
		return paths, fmt.Errorf("restrict runtime directory: %w", err)
	}
	return paths, nil
}

// DeleteRuntimeDirectory 安全删除单个 sandbox 的受管 runtime directory。
//
// 删除目标必须精确等于 `<dataDirectory>/run/<sandboxID>`；函数拒绝 run root
// 或目标 symlink、非目录占位和越界 ID。RemoveAll 只用于已经完成上述证明的
// ID 子目录，永远不会用于 data root 或 run root。
func DeleteRuntimeDirectory(
	dataDirectory string,
	sandboxID string,
) error {
	names, err := NamesForSandbox(dataDirectory, sandboxID)
	if err != nil {
		return err
	}
	runRoot := filepath.Dir(names.RuntimeDirectory)
	relative, err := filepath.Rel(runRoot, names.RuntimeDirectory)
	if err != nil ||
		relative != sandboxID ||
		filepath.Dir(names.RuntimeDirectory) != runRoot {
		return errors.New("runtime directory is outside managed run root")
	}

	if err := requireRealDirectory(runRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("validate runtime root before delete: %w", err)
	}
	info, err := os.Lstat(names.RuntimeDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime directory before delete: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime directory is a symlink")
	}
	if !info.IsDir() {
		return errors.New("runtime directory is not a directory")
	}
	if err := os.RemoveAll(names.RuntimeDirectory); err != nil {
		return fmt.Errorf("remove runtime directory: %w", err)
	}
	return nil
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
