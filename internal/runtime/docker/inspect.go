package docker

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// Inspect 按确定性容器名读取并验证 sandbox 的 Docker 实际状态。
//
// 容器缺失是正常观测结果而非错误；存在的容器必须通过名称和恢复 labels
// 校验。返回模型不包含 Docker SDK struct，也不探测 runner readiness。
func (r *Runtime) Inspect(
	ctx context.Context,
	sandboxID string,
) (runtimeport.ActualSandbox, error) {
	if !validSandboxID(sandboxID) {
		return runtimeport.ActualSandbox{}, fmt.Errorf(
			"sandbox ID is invalid: %w",
			domain.ErrInvalid,
		)
	}
	name := containerName(sandboxID)
	inspection, err := r.engine.ContainerInspect(
		ctx,
		name,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return runtimeport.ActualSandbox{
				ID:    sandboxID,
				State: runtimeport.ActualMissing,
			}, nil
		}
		return runtimeport.ActualSandbox{}, runtimeUnavailable(err)
	}

	metadata, err := inspectManagedContainer(
		inspection.Container,
		name,
		sandboxID,
	)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	state, err := mapContainerState(inspection.Container.State)
	if err != nil {
		return runtimeport.ActualSandbox{}, err
	}
	return runtimeport.ActualSandbox{
		ID:        metadata.SandboxID,
		RuntimeID: inspection.Container.ID,
		State:     state,
		SpecHash:  metadata.SpecHash,
		Workspace: metadata.Workspace,
	}, nil
}

// inspectManagedContainer 校验单容器观测所需的名称、ID 和恢复 labels。
func inspectManagedContainer(
	container mobycontainer.InspectResponse,
	expectedName string,
	expectedSandboxID string,
) (ManagedLabels, error) {
	if container.ID == "" ||
		strings.TrimPrefix(container.Name, "/") != expectedName ||
		container.Config == nil {
		return ManagedLabels{}, containerIdentityConflict()
	}
	metadata, err := ParseLabels(container.Config.Labels)
	if err != nil ||
		metadata.SandboxID != expectedSandboxID ||
		metadata.Workspace != workspaceName(expectedSandboxID) {
		return ManagedLabels{}, containerIdentityConflict()
	}
	return metadata, nil
}

// mapContainerState 把 Docker 状态收敛为 runtime port 的稳定四态模型。
func mapContainerState(
	state *mobycontainer.State,
) (runtimeport.ActualState, error) {
	if state == nil {
		return "", containerStateConflict()
	}
	switch state.Status {
	case mobycontainer.StateCreated:
		return runtimeport.ActualCreated, nil
	case mobycontainer.StateRunning:
		return runtimeport.ActualRunning, nil
	case mobycontainer.StateExited, mobycontainer.StateDead:
		return runtimeport.ActualStopped, nil
	default:
		return "", containerStateConflict()
	}
}
