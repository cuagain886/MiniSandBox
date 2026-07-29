package runtime

import (
	"context"
	"errors"

	"minisandbox/internal/domain"
)

const (
	// FailureReasonImagePullFailed 表示 sandbox image 拉取失败。
	FailureReasonImagePullFailed = "IMAGE_PULL_FAILED"
	// FailureReasonArtifactInvalid 表示 runner/init artifact 缺失或平台不兼容。
	FailureReasonArtifactInvalid = "ARTIFACT_INVALID"
	// FailureReasonContainerCreateFailed 表示 Docker container 创建失败。
	FailureReasonContainerCreateFailed = "CONTAINER_CREATE_FAILED"
	// FailureReasonArtifactInjectionFailed 表示 artifact 复制到 container 失败。
	FailureReasonArtifactInjectionFailed = "ARTIFACT_INJECTION_FAILED"
	// FailureReasonContainerStartFailed 表示已准备 container 启动失败。
	FailureReasonContainerStartFailed = "CONTAINER_START_FAILED"
	// FailureReasonRunnerUnhealthy 表示 runner 未在时限内就绪。
	FailureReasonRunnerUnhealthy = "RUNNER_UNHEALTHY"
	// FailureReasonSpecDrift 表示实际资源身份或 spec hash 与 Store 不一致。
	FailureReasonSpecDrift = "SPEC_DRIFT"
	// FailureReasonCleanupPending 表示受管资源尚未完全清理。
	FailureReasonCleanupPending = "CLEANUP_PENDING"
	// FailureReasonRuntimeUnavailable 表示 Docker 等 runtime 依赖暂时不可用。
	FailureReasonRuntimeUnavailable = "RUNTIME_UNAVAILABLE"
	// FailureReasonInternalError 表示无法安全归类的内部错误。
	FailureReasonInternalError = "INTERNAL_ERROR"
)

// Failure 保存可写入 observed state 的安全失败分类。
type Failure struct {
	// Reason 是稳定机器可读 reason，只能取受控 allowlist 中的值。
	Reason string
	// Message 是不含底层 cause、路径、凭据和内部堆栈的固定文案。
	Message string
	// Retryable 表示后续 reconcile 是否可以再次尝试。
	Retryable bool
}

type failureReasonError interface {
	FailureReason() string
}

type unavailableError interface {
	Unavailable() bool
}

var failureCatalog = map[string]Failure{
	FailureReasonImagePullFailed: {
		Reason:    FailureReasonImagePullFailed,
		Message:   "Failed to pull sandbox image.",
		Retryable: true,
	},
	FailureReasonArtifactInvalid: {
		Reason:    FailureReasonArtifactInvalid,
		Message:   "Sandbox runtime artifacts are invalid.",
		Retryable: false,
	},
	FailureReasonContainerCreateFailed: {
		Reason:    FailureReasonContainerCreateFailed,
		Message:   "Failed to create sandbox container.",
		Retryable: true,
	},
	FailureReasonArtifactInjectionFailed: {
		Reason:    FailureReasonArtifactInjectionFailed,
		Message:   "Failed to inject sandbox runtime artifacts.",
		Retryable: true,
	},
	FailureReasonContainerStartFailed: {
		Reason:    FailureReasonContainerStartFailed,
		Message:   "Failed to start sandbox container.",
		Retryable: true,
	},
	FailureReasonRunnerUnhealthy: {
		Reason:    FailureReasonRunnerUnhealthy,
		Message:   "Sandbox runner is unhealthy.",
		Retryable: true,
	},
	FailureReasonSpecDrift: {
		Reason:    FailureReasonSpecDrift,
		Message:   "Sandbox runtime does not match the persisted specification.",
		Retryable: false,
	},
	FailureReasonCleanupPending: {
		Reason:    FailureReasonCleanupPending,
		Message:   "Sandbox runtime cleanup is pending.",
		Retryable: true,
	},
	FailureReasonRuntimeUnavailable: {
		Reason:    FailureReasonRuntimeUnavailable,
		Message:   "Sandbox runtime is temporarily unavailable.",
		Retryable: true,
	},
	FailureReasonInternalError: {
		Reason:    FailureReasonInternalError,
		Message:   "An unexpected internal error occurred.",
		Retryable: true,
	},
}

// ClassifyError 使用 typed marker 和 errors.Is/As 生成稳定失败语义。
//
// 本函数从不解析 Error() 字符串，也不把底层 cause 拼入 Message。未知错误
// 固定映射 INTERNAL_ERROR；显式 dependency unavailable 和 deadline 映射
// RUNTIME_UNAVAILABLE。
func ClassifyError(err error) Failure {
	if err == nil {
		return Failure{}
	}
	var reasoned failureReasonError
	if errors.As(err, &reasoned) {
		if failure, ok := failureCatalog[reasoned.FailureReason()]; ok {
			return failure
		}
	}
	var unavailable unavailableError
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &unavailable) && unavailable.Unavailable() {
		return failureCatalog[FailureReasonRuntimeUnavailable]
	}
	if errors.Is(err, domain.ErrConflict) {
		return failureCatalog[FailureReasonSpecDrift]
	}
	return failureCatalog[FailureReasonInternalError]
}
