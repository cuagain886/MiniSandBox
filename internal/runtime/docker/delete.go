package docker

import (
	"context"
	"errors"
	"math"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

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
