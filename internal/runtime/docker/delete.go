package docker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/testcrashpoint"
)

const defaultContainerStopTimeout = 10 * time.Second

// Delete 按 container、workspace volume、runtime directory 的固定顺序清理。
//
// 三个步骤均按 best-effort 执行并使用 errors.Join 保留所有未完成项；重复
// 调用会由各原子 helper 从实际状态继续。只有三类资源均确认不存在才返回 nil。
func (r *Runtime) Delete(ctx context.Context, sandboxID string) (resultErr error) {
	defer func() { r.observeDocker("delete_sandbox", resultErr) }()
	if r == nil || r.engine == nil {
		return errors.New("docker runtime is not initialized")
	}
	if !validSandboxID(sandboxID) {
		return errors.New("sandbox ID is invalid")
	}

	var failures []error
	if err := deleteManagedContainer(
		ctx,
		r.engine,
		sandboxID,
		defaultContainerStopTimeout,
	); err != nil {
		failures = append(failures, fmt.Errorf("delete container: %w", err))
	} else if engine, ok := r.engine.(EgressEngine); ok {
		testcrashpoint.Hit("delete.container-remove")
		// 只有主容器已确认不存在后才能移除 namespace anchor，避免在途 execution 突然落入不同网络语义。
		if err := deleteManagedEgressSidecar(ctx, engine, sandboxID, defaultContainerStopTimeout); err != nil {
			failures = append(failures, fmt.Errorf("delete egress sidecar: %w", err))
		}
	}
	if err := deleteWorkspaceVolume(ctx, r.engine, sandboxID); err != nil {
		failures = append(
			failures,
			fmt.Errorf("delete workspace volume: %w", err),
		)
	} else {
		testcrashpoint.Hit("delete.volume-remove")
	}
	if err := DeleteRuntimeDirectory(r.dataDirectory, sandboxID); err != nil {
		failures = append(
			failures,
			fmt.Errorf("delete runtime directory: %w", err),
		)
	} else {
		testcrashpoint.Hit("delete.runtime-directory-remove")
	}
	return errors.Join(failures...)
}

func deleteManagedEgressSidecar(ctx context.Context, engine EgressEngine, sandboxID string, stopTimeout time.Duration) error {
	timeoutSeconds, err := stopTimeoutSeconds(stopTimeout)
	if err != nil {
		return err
	}
	name := egressSidecarName(sandboxID)
	inspection, err := engine.ContainerInspect(ctx, name, mobyclient.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return runtimeUnavailable(err)
	}
	container := inspection.Container
	labels := map[string]string(nil)
	if container.Config != nil {
		labels = container.Config.Labels
	}
	if container.ID == "" || strings.TrimPrefix(container.Name, "/") != name || labels[LabelManaged] != labelManagedValue || labels[LabelSchemaVersion] != labelSchemaVersionValue || labels[LabelSandboxID] != sandboxID || labels[LabelResourceRole] != resourceRoleEgressSidecar {
		return containerIdentityConflict()
	}
	force := false
	if containerNeedsStop(container.State) {
		_, stopErr := engine.ContainerStop(ctx, container.ID, mobyclient.ContainerStopOptions{Timeout: &timeoutSeconds})
		if cerrdefs.IsNotFound(stopErr) {
			return nil
		}
		force = stopErr != nil
	}
	_, err = engine.ContainerRemove(ctx, container.ID, mobyclient.ContainerRemoveOptions{Force: force})
	if err == nil || cerrdefs.IsNotFound(err) {
		return nil
	}
	return runtimeUnavailable(err)
}

// deleteManagedContainer 幂等停止并删除单个经过身份验证的 sandbox 容器。
//
// 优雅 stop 失败后才使用 force remove；无论哪条路径都不删除 volume，
// 同名非受管或 labels 损坏的容器会在产生删除副作用前返回 conflict。
func deleteManagedContainer(
	ctx context.Context,
	engine Engine,
	sandboxID string,
	stopTimeout time.Duration,
) error {
	if !validSandboxID(sandboxID) {
		return errors.New("sandbox ID is invalid")
	}
	timeoutSeconds, err := stopTimeoutSeconds(stopTimeout)
	if err != nil {
		return err
	}
	name := containerName(sandboxID)
	inspection, err := engine.ContainerInspect(
		ctx,
		name,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return runtimeUnavailable(err)
	}
	if _, err := inspectManagedContainer(
		inspection.Container,
		name,
		sandboxID,
	); err != nil {
		return err
	}

	force := false
	if containerNeedsStop(inspection.Container.State) {
		_, stopErr := engine.ContainerStop(
			ctx,
			inspection.Container.ID,
			mobyclient.ContainerStopOptions{Timeout: &timeoutSeconds},
		)
		if cerrdefs.IsNotFound(stopErr) {
			return nil
		}
		// Stop API 已在 timeout 后发送 SIGKILL；若 daemon 仍返回错误，唯一
		// 明确 fallback 是对同一已验证 ID 执行 force remove。
		force = stopErr != nil
	}
	_, err = engine.ContainerRemove(
		ctx,
		inspection.Container.ID,
		mobyclient.ContainerRemoveOptions{
			Force:         force,
			RemoveVolumes: false,
			RemoveLinks:   false,
		},
	)
	if err == nil || cerrdefs.IsNotFound(err) {
		return nil
	}
	return runtimeUnavailable(err)
}

// containerNeedsStop 判断容器是否仍可能持有活动主进程。
func containerNeedsStop(state *mobycontainer.State) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case mobycontainer.StateRunning,
		mobycontainer.StatePaused,
		mobycontainer.StateRestarting:
		return true
	default:
		return state.Running
	}
}

// stopTimeoutSeconds 向上取整为 Docker Stop API 的正秒数。
func stopTimeoutSeconds(timeout time.Duration) (int, error) {
	if timeout <= 0 {
		return 0, errors.New("container stop timeout must be positive")
	}
	seconds := timeout / time.Second
	if timeout%time.Second != 0 {
		seconds++
	}
	if seconds > time.Duration(math.MaxInt) {
		return 0, errors.New("container stop timeout is too large")
	}
	return int(seconds), nil
}
