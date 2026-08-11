package reconcile

import (
	"context"
	"errors"
	"net/http"

	"minisandbox/internal/domain"
	"minisandbox/internal/runnerclient"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

// RetryClassification 是不含原始 cause 的 typed error 分类结果。
type RetryClassification struct {
	// ErrorClass 可直接写入 RetryPolicyInput；成功结果为空。
	ErrorClass RetryErrorClass
	// Successful 表示 nil 或收敛删除目标已不存在，无需失败记账。
	Successful bool
	// Reason 是已有生命周期 allowlist 中的安全机器码；无对应状态时为空。
	Reason string
}

type retryFailureReason interface{ FailureReason() string }
type retryUnavailable interface{ Unavailable() bool }

// ClassifyRetryError 使用 errors.Is/As 把 Store、Runtime 和 Runner 错误映射为稳定类别。
func ClassifyRetryError(operation RetryOperation, err error) RetryClassification {
	if err == nil {
		return RetryClassification{Successful: true}
	}
	if errors.Is(err, context.Canceled) {
		return RetryClassification{ErrorClass: RetryErrorShutdown}
	}

	// 先识别 typed runtime reason；SpecDriftError 会 unwrap ErrConflict，但它是永久
	// 规格漂移而不是 Store CAS snapshot 过期。
	var reasoned retryFailureReason
	if errors.As(err, &reasoned) {
		failure := runtimeport.ClassifyError(err)
		if failure.Reason != "" {
			class := RetryErrorPermanent
			if failure.Retryable {
				class = RetryErrorTransient
			}
			return RetryClassification{ErrorClass: class, Reason: failure.Reason}
		}
	}
	if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrLeaseConflict) {
		return RetryClassification{ErrorClass: RetryErrorConflict}
	}
	if errors.Is(err, domain.ErrNotFound) {
		if operation == RetryOperationDelete || operation == RetryOperationExpire || operation == RetryOperationCleanup {
			return RetryClassification{ErrorClass: RetryErrorAlreadyAbsent, Successful: true}
		}
		return RetryClassification{ErrorClass: RetryErrorTransient}
	}

	var protocolMismatch *runnerclient.ProtocolMismatchError
	var authentication *runnerclient.AuthenticationError
	if errors.As(err, &protocolMismatch) || errors.As(err, &authentication) ||
		errors.Is(err, domain.ErrRunnerProtocolMismatch) ||
		errors.Is(err, domain.ErrEgressPolicyInvalid) || errors.Is(err, domain.ErrOutboundNotAllowed) ||
		errors.Is(err, storeport.ErrCorrupt) || errors.Is(err, domain.ErrInvalid) {
		return RetryClassification{ErrorClass: RetryErrorPermanent}
	}
	var status *runnerclient.StatusError
	if errors.As(err, &status) {
		if status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden || status.StatusCode < 500 {
			return RetryClassification{ErrorClass: RetryErrorPermanent}
		}
		return RetryClassification{ErrorClass: RetryErrorTransient}
	}
	var connection *runnerclient.ConnectionError
	var socketMissing *runnerclient.SocketMissingError
	var unhealthy *runnerclient.UnhealthyError
	var timeout *runnerclient.TimeoutError
	var unavailable retryUnavailable
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &connection) || errors.As(err, &socketMissing) ||
		errors.As(err, &unhealthy) || errors.As(err, &timeout) ||
		errors.As(err, &unavailable) && unavailable.Unavailable() ||
		errors.Is(err, domain.ErrRunnerUnhealthy) || errors.Is(err, domain.ErrEgressNotReady) ||
		errors.Is(err, domain.ErrEgressUnhealthy) || errors.Is(err, domain.ErrEgressImageUnavailable) {
		return RetryClassification{ErrorClass: RetryErrorTransient}
	}

	// 未知错误不解析文本；保守进入有界 backoff，尤其不能让 must-converge 清理静默停止。
	return RetryClassification{ErrorClass: RetryErrorTransient}
}
