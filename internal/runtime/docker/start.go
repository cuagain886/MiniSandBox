package docker

import (
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
)

// RuntimeMissingError 表示期望启动的 Docker 容器已经不存在。
//
// 该错误满足 errors.Is(err, domain.ErrNotFound)，供 reconciler 区分“重新
// Ensure”与 daemon 暂时不可用；错误文本不包含容器 ID。
type RuntimeMissingError struct{}

// Error 返回不泄露 Docker 标识的固定文案。
func (*RuntimeMissingError) Error() string {
	return "sandbox runtime is missing"
}

// Unwrap 把缺失语义映射到稳定领域错误。
func (*RuntimeMissingError) Unwrap() error {
	return domain.ErrNotFound
}

// startContainer 幂等启动已经准备完成的 stopped container。
//
// running 直接成功；只有 created/exited 状态允许调用 start，避免对 paused、
// restarting、removing 或 dead 状态执行含义不明确的修复。
func startContainer(
	ctx context.Context,
	engine Engine,
	containerID string,
) error {
	if containerID == "" {
		return &ContainerStartFailedError{cause: &RuntimeMissingError{}}
	}
	inspection, err := engine.ContainerInspect(
		ctx,
		containerID,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return &ContainerStartFailedError{cause: &RuntimeMissingError{}}
		}
		return runtimeUnavailable(err)
	}
	if inspection.Container.State == nil {
		return &ContainerStartFailedError{cause: containerStateConflict()}
	}
	switch inspection.Container.State.Status {
	case mobycontainer.StateRunning:
		return nil
	case mobycontainer.StateCreated, mobycontainer.StateExited:
	default:
		return &ContainerStartFailedError{cause: containerStateConflict()}
	}

	_, err = engine.ContainerStart(
		ctx,
		containerID,
		mobyclient.ContainerStartOptions{},
	)
	if err == nil {
		return nil
	}
	// inspect 与 start 之间可能有同一 sandbox 的重试完成启动；Docker 的
	// NotModified 正表示期望 running 状态已达成，因此必须按幂等成功处理。
	if cerrdefs.IsNotModified(err) {
		return nil
	}
	if cerrdefs.IsNotFound(err) {
		return &ContainerStartFailedError{cause: &RuntimeMissingError{}}
	}
	return &ContainerStartFailedError{cause: err}
}

// containerStateConflict 返回不泄露 Docker 原始状态的固定冲突错误。
func containerStateConflict() error {
	return fmt.Errorf(
		"container state cannot be started safely: %w",
		domain.ErrConflict,
	)
}
