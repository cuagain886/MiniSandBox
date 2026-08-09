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
	// errUnsupportedSandboxStatus 表示已知 state 和 reason 形成了未冻结组合。
	errUnsupportedSandboxStatus = errors.New("unsupported sandbox status")
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
	if !domain.SandboxReasonStateAllowed(sandbox.Reason, sandbox.ObservedState) {
		return protocol.Sandbox{}, errUnsupportedSandboxStatus
	}
	message, ok := domain.SandboxReasonPublicMessage(sandbox.Reason)
	if !ok {
		return protocol.Sandbox{}, errUnsupportedSandboxReason
	}
	return protocol.Sandbox{
		ID:        sandbox.ID,
		State:     state,
		Reason:    reason,
		Message:   message,
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
	case domain.SandboxReasonCreateAccepted:
		return protocol.SandboxReasonCreateAccepted, nil
	case domain.SandboxReasonCreatingRuntime:
		return protocol.SandboxReasonCreatingRuntime, nil
	case domain.SandboxReasonWaitingRunner:
		return protocol.SandboxReasonWaitingRunner, nil
	case domain.SandboxReasonRunning:
		return protocol.SandboxReasonRunning, nil
	case domain.SandboxReasonDeleteAccepted:
		return protocol.SandboxReasonDeleteAccepted, nil
	case domain.SandboxReasonDeletingRuntime:
		return protocol.SandboxReasonDeletingRuntime, nil
	case domain.SandboxReasonTerminated:
		return protocol.SandboxReasonTerminated, nil
	case domain.SandboxReasonImagePullFailed:
		return protocol.SandboxReasonImagePullFailed, nil
	case domain.SandboxReasonArtifactInvalid:
		return protocol.SandboxReasonArtifactInvalid, nil
	case domain.SandboxReasonContainerCreateFailed:
		return protocol.SandboxReasonContainerCreateFailed, nil
	case domain.SandboxReasonArtifactInjectionFailed:
		return protocol.SandboxReasonArtifactInjectionFailed, nil
	case domain.SandboxReasonContainerStartFailed:
		return protocol.SandboxReasonContainerStartFailed, nil
	case domain.SandboxReasonRunnerUnhealthy:
		return protocol.SandboxReasonRunnerUnhealthy, nil
	case domain.SandboxReasonRunnerProtocolMismatch:
		return protocol.SandboxReasonRunnerProtocolMismatch, nil
	case domain.SandboxReasonEgressUnhealthy:
		return protocol.SandboxReasonEgressUnhealthy, nil
	case domain.SandboxReasonSpecDrift:
		return protocol.SandboxReasonSpecDrift, nil
	case domain.SandboxReasonCleanupPending:
		return protocol.SandboxReasonCleanupPending, nil
	case domain.SandboxReasonRuntimeUnavailable:
		return protocol.SandboxReasonRuntimeUnavailable, nil
	case domain.SandboxReasonInternalError:
		return protocol.SandboxReasonInternalError, nil
	case domain.SandboxReasonRetryScheduled:
		return protocol.SandboxReasonRetryScheduled, nil
	case domain.SandboxReasonRecoveringRuntime:
		return protocol.SandboxReasonRecoveringRuntime, nil
	case domain.SandboxReasonRunnerHealthDegraded:
		return protocol.SandboxReasonRunnerHealthDegraded, nil
	case domain.SandboxReasonTTLExpired:
		return protocol.SandboxReasonTTLExpired, nil
	case domain.SandboxReasonOrphanImported:
		return protocol.SandboxReasonOrphanImported, nil
	case domain.SandboxReasonOrphanExpired:
		return protocol.SandboxReasonOrphanExpired, nil
	default:
		return "", errUnsupportedSandboxReason
	}
}
