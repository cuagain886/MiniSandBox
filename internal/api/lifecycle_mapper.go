package api

import (
	"errors"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

var (
	// errUnsupportedSandboxState 表示内部状态无法安全映射到公共协议。
	errUnsupportedSandboxState = errors.New("unsupported sandbox state")
	// errUnsupportedSandboxReason 表示内部 reason 不属于已冻结公共枚举。
	errUnsupportedSandboxReason = errors.New("unsupported sandbox reason")
)

// mapSandboxResponse 把领域 Sandbox 显式转换为安全的公共响应。
//
// 本函数只复制公共契约允许的字段，不暴露 runtime ID、spec hash、revision、
// resolved resources、网络配置或宿主机恢复元数据。
func mapSandboxResponse(sandbox domain.Sandbox) (protocol.Sandbox, error) {
	state, err := mapSandboxState(sandbox.ObservedState)
	if err != nil {
		return protocol.Sandbox{}, err
	}
	reason, err := mapSandboxReason(sandbox.Reason)
	if err != nil {
		return protocol.Sandbox{}, err
	}
	return protocol.Sandbox{
		ID:        sandbox.ID,
		State:     state,
		Reason:    reason,
		Message:   sandbox.Message,
		Image:     sandbox.Spec.Image,
		CreatedAt: sandbox.CreatedAt.UTC(),
		UpdatedAt: sandbox.UpdatedAt.UTC(),
	}, nil
}

// mapSandboxState 只接受公共协议已经冻结的领域状态。
func mapSandboxState(state domain.SandboxState) (protocol.SandboxState, error) {
	switch state {
	case domain.StatePending:
		return protocol.SandboxStatePending, nil
	case domain.StateCreating:
		return protocol.SandboxStateCreating, nil
	case domain.StateRunning:
		return protocol.SandboxStateRunning, nil
	case domain.StateStopping:
		return protocol.SandboxStateStopping, nil
	case domain.StateTerminated:
		return protocol.SandboxStateTerminated, nil
	case domain.StateFailed:
		return protocol.SandboxStateFailed, nil
	default:
		return "", errUnsupportedSandboxState
	}
}

// mapSandboxReason 只接受公共协议已经冻结的生命周期原因。
func mapSandboxReason(reason string) (protocol.SandboxReason, error) {
	switch reason {
	case string(protocol.SandboxReasonCreateAccepted):
		return protocol.SandboxReasonCreateAccepted, nil
	case string(protocol.SandboxReasonCreatingRuntime):
		return protocol.SandboxReasonCreatingRuntime, nil
	case string(protocol.SandboxReasonWaitingRunner):
		return protocol.SandboxReasonWaitingRunner, nil
	case string(protocol.SandboxReasonRunning):
		return protocol.SandboxReasonRunning, nil
	case string(protocol.SandboxReasonDeleteAccepted):
		return protocol.SandboxReasonDeleteAccepted, nil
	case string(protocol.SandboxReasonDeletingRuntime):
		return protocol.SandboxReasonDeletingRuntime, nil
	case string(protocol.SandboxReasonTerminated):
		return protocol.SandboxReasonTerminated, nil
	case string(protocol.SandboxReasonImagePullFailed):
		return protocol.SandboxReasonImagePullFailed, nil
	case string(protocol.SandboxReasonArtifactInvalid):
		return protocol.SandboxReasonArtifactInvalid, nil
	case string(protocol.SandboxReasonContainerCreateFailed):
		return protocol.SandboxReasonContainerCreateFailed, nil
	case string(protocol.SandboxReasonArtifactInjectionFailed):
		return protocol.SandboxReasonArtifactInjectionFailed, nil
	case string(protocol.SandboxReasonContainerStartFailed):
		return protocol.SandboxReasonContainerStartFailed, nil
	case string(protocol.SandboxReasonRunnerUnhealthy):
		return protocol.SandboxReasonRunnerUnhealthy, nil
	case string(protocol.SandboxReasonSpecDrift):
		return protocol.SandboxReasonSpecDrift, nil
	case string(protocol.SandboxReasonCleanupPending):
		return protocol.SandboxReasonCleanupPending, nil
	case string(protocol.SandboxReasonRuntimeUnavailable):
		return protocol.SandboxReasonRuntimeUnavailable, nil
	case string(protocol.SandboxReasonInternalError):
		return protocol.SandboxReasonInternalError, nil
	default:
		return "", errUnsupportedSandboxReason
	}
}
