package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
	"minisandbox/pkg/protocol"
)

// unavailableError 是依赖不可用分类的结构化 marker。
//
// application 或 runtime 的 typed error 可实现本接口，无需依赖 HTTP 包；mapper
// 不解析错误字符串。
type unavailableError interface {
	Unavailable() bool
}

// errorMapping 保存内部错误对应的固定公共语义。
type errorMapping struct {
	status    int
	code      string
	message   string
	retryable bool
}

// notImplemented 返回遵循统一 mapper 的占位 handler。
func notImplemented(_ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, domain.ErrNotImplemented)
	}
}

// writeError 把内部错误写为统一公共 envelope。
//
// request ID 优先读取 middleware 已设置的响应头，便于直接 handler 测试时也
// 回退到请求头。500/503 日志只记录错误具体类型，不调用可能包含秘密的
// Error() 文本。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	requestID := w.Header().Get(requestIDHeader)
	if contextual, ok := logging.RequestIDFromContext(r.Context()); ok {
		requestID = contextual.String()
		w.Header().Set(requestIDHeader, requestID)
	}
	if requestID == "" {
		requestID = r.Header.Get(requestIDHeader)
		if requestID != "" {
			w.Header().Set(requestIDHeader, requestID)
		}
	}

	mapping := mapError(err)
	if mapping.status >= http.StatusInternalServerError {
		slog.Error(
			"request failed",
			"request_id",
			requestID,
			"status",
			mapping.status,
			"error_type",
			fmt.Sprintf("%T", err),
		)
	}
	writeJSON(w, mapping.status, protocol.ErrorResponse{
		Error: protocol.ErrorDetail{
			Code:      mapping.code,
			Message:   mapping.message,
			RequestID: requestID,
			Retryable: mapping.retryable,
		},
	})
}

// mapError 按 errors.Is/As 分类内部错误并返回固定公共语义。
func mapError(err error) errorMapping {
	switch {
	case errors.Is(err, domain.ErrInvalidTTL):
		return errorMapping{
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidTTL),
			message:   "Sandbox TTL is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrInvalidExpiration):
		return errorMapping{
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidExpiration),
			message:   "Sandbox expiration is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrLeaseConflict):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeLeaseConflict),
			message:   "Sandbox lease conflicts with the current expiration.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrSandboxExpiring):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeSandboxExpiring),
			message:   "Sandbox is expiring or terminating.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeIdempotencyConflict),
			message:   "Idempotency key conflicts with a different request.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrSandboxLimitReached):
		return errorMapping{
			status:    http.StatusTooManyRequests,
			code:      string(protocol.ErrorCodeSandboxLimitReached),
			message:   "Sandbox limit has been reached.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrAdminDisabled):
		return errorMapping{
			status:    http.StatusNotFound,
			code:      string(protocol.ErrorCodeAdminDisabled),
			message:   "Admin API is not available.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrInvalidExecutionRequest):
		return errorMapping{
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidExecutionRequest),
			message:   "Execution request is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrSandboxNotRunning):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeSandboxNotRunning),
			message:   "Sandbox is not ready to execute commands.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrExecutionNotFound):
		return errorMapping{
			status:    http.StatusNotFound,
			code:      string(protocol.ErrorCodeExecutionNotFound),
			message:   "Execution does not exist.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrExecutionLimitReached):
		return errorMapping{
			status:    http.StatusTooManyRequests,
			code:      string(protocol.ErrorCodeExecutionLimitReached),
			message:   "Execution concurrency limit has been reached.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrShellNotFound):
		return errorMapping{
			status:    http.StatusUnprocessableEntity,
			code:      string(protocol.ErrorCodeShellNotFound),
			message:   "Requested shell is unavailable.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrInvalidCWD):
		return errorMapping{
			status:    http.StatusUnprocessableEntity,
			code:      string(protocol.ErrorCodeInvalidCWD),
			message:   "Execution working directory is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrRunnerUnhealthy):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeRunnerUnhealthy),
			message:   "Sandbox runner is unavailable.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrRunnerProtocolMismatch):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeRunnerProtocolMismatch),
			message:   "Sandbox runner protocol is incompatible.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrFilesUnavailable):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeFilesUnavailable),
			message:   "Sandbox files capability is unavailable.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrInvalidFilePath):
		return errorMapping{
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidFilePath),
			message:   "Workspace file path is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrFileNotFound):
		return errorMapping{
			status:    http.StatusNotFound,
			code:      string(protocol.ErrorCodeFileNotFound),
			message:   "Workspace file does not exist.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrFileTypeMismatch):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeFileTypeMismatch),
			message:   "Workspace file type does not match the operation.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrFileConflict):
		return errorMapping{
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeFileConflict),
			message:   "Workspace file conflicts with an existing entry.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrFileTooLarge):
		return errorMapping{
			status:    http.StatusRequestEntityTooLarge,
			code:      string(protocol.ErrorCodeFileTooLarge),
			message:   "Workspace file exceeds the configured size limit.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrOutboundNotAllowed):
		return errorMapping{
			status:    http.StatusForbidden,
			code:      string(protocol.ErrorCodeOutboundNotAllowed),
			message:   "Outbound network access is not allowed.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrEgressImageUnavailable):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressImageUnavailable),
			message:   "Sandbox egress image is unavailable.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrEgressPolicyInvalid):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressPolicyInvalid),
			message:   "Sandbox egress policy is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrEgressNotReady):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressNotReady),
			message:   "Sandbox egress is not ready.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrEgressUnhealthy):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressUnhealthy),
			message:   "Sandbox egress is unhealthy.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrInvalid):
		return errorMapping{
			status:    http.StatusBadRequest,
			code:      "INVALID_REQUEST",
			message:   "Request is invalid.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrNotFound):
		return errorMapping{
			status:    http.StatusNotFound,
			code:      "SANDBOX_NOT_FOUND",
			message:   "Sandbox does not exist.",
			retryable: false,
		}
	case errors.Is(err, domain.ErrConflict):
		return errorMapping{
			status:    http.StatusConflict,
			code:      "SANDBOX_CONFLICT",
			message:   "Request conflicts with the current sandbox state.",
			retryable: true,
		}
	case errors.Is(err, domain.ErrNotImplemented):
		return errorMapping{
			status:    http.StatusNotImplemented,
			code:      "NOT_IMPLEMENTED",
			message:   "Requested operation is not implemented.",
			retryable: false,
		}
	case errors.Is(err, context.DeadlineExceeded), isUnavailable(err):
		return errorMapping{
			status:    http.StatusServiceUnavailable,
			code:      "RUNTIME_UNAVAILABLE",
			message:   "A required control-plane dependency is unavailable.",
			retryable: true,
		}
	default:
		return errorMapping{
			status:    http.StatusInternalServerError,
			code:      "INTERNAL_ERROR",
			message:   "An unexpected internal error occurred.",
			retryable: true,
		}
	}
}

// isUnavailable 判断错误链中是否存在显式依赖不可用 marker。
func isUnavailable(err error) bool {
	var unavailable unavailableError
	return errors.As(err, &unavailable) && unavailable.Unavailable()
}
