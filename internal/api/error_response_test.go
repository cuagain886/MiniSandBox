package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// testUnavailableError 模拟 application/runtime 的结构化依赖不可用错误。
type testUnavailableError struct {
	cause error
}

// Error 返回底层测试 cause；公共 mapper 不得回显该文本。
func (e *testUnavailableError) Error() string {
	return e.cause.Error()
}

// Unwrap 保留内部 cause 链。
func (e *testUnavailableError) Unwrap() error {
	return e.cause
}

// Unavailable 标记该错误应映射为可重试 503。
func (e *testUnavailableError) Unavailable() bool {
	return true
}

// TestWriteErrorMappings 验证 400、404、409、500 和 503 的固定公共语义。
func TestWriteErrorMappings(t *testing.T) {
	const secret = "secret-docker-socket-and-token"
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		message   string
		retryable bool
	}{
		{
			name:      "invalid",
			err:       fmt.Errorf("validation context: %w", domain.ErrInvalid),
			status:    http.StatusBadRequest,
			code:      "INVALID_REQUEST",
			message:   "Request is invalid.",
			retryable: false,
		},
		{
			name:      "invalid TTL",
			err:       fmt.Errorf("ttl=%s: %w", secret, domain.ErrInvalidTTL),
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidTTL),
			message:   "Sandbox TTL is invalid.",
			retryable: false,
		},
		{
			name:      "invalid expiration",
			err:       fmt.Errorf("expiration=%s: %w", secret, domain.ErrInvalidExpiration),
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidExpiration),
			message:   "Sandbox expiration is invalid.",
			retryable: false,
		},
		{
			name:      "lease conflict",
			err:       fmt.Errorf("lease=%s: %w", secret, domain.ErrLeaseConflict),
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeLeaseConflict),
			message:   "Sandbox lease conflicts with the current expiration.",
			retryable: false,
		},
		{
			name:      "sandbox expiring",
			err:       fmt.Errorf("state=%s: %w", secret, domain.ErrSandboxExpiring),
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeSandboxExpiring),
			message:   "Sandbox is expiring or terminating.",
			retryable: false,
		},
		{
			name:      "idempotency conflict",
			err:       fmt.Errorf("key=%s: %w", secret, domain.ErrIdempotencyConflict),
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeIdempotencyConflict),
			message:   "Idempotency key conflicts with a different request.",
			retryable: false,
		},
		{
			name:      "sandbox limit reached",
			err:       fmt.Errorf("tenant=%s: %w", secret, domain.ErrSandboxLimitReached),
			status:    http.StatusTooManyRequests,
			code:      string(protocol.ErrorCodeSandboxLimitReached),
			message:   "Sandbox limit has been reached.",
			retryable: true,
		},
		{
			name:      "admin disabled",
			err:       fmt.Errorf("config=%s: %w", secret, domain.ErrAdminDisabled),
			status:    http.StatusNotFound,
			code:      string(protocol.ErrorCodeAdminDisabled),
			message:   "Admin API is not available.",
			retryable: false,
		},
		{
			name:      "invalid execution request",
			err:       fmt.Errorf("argv=%s: %w", secret, domain.ErrInvalidExecutionRequest),
			status:    http.StatusBadRequest,
			code:      string(protocol.ErrorCodeInvalidExecutionRequest),
			message:   "Execution request is invalid.",
			retryable: false,
		},
		{
			name:      "sandbox not running",
			err:       domain.ErrSandboxNotRunning,
			status:    http.StatusConflict,
			code:      string(protocol.ErrorCodeSandboxNotRunning),
			message:   "Sandbox is not ready to execute commands.",
			retryable: true,
		},
		{
			name:      "execution not found",
			err:       domain.ErrExecutionNotFound,
			status:    http.StatusNotFound,
			code:      string(protocol.ErrorCodeExecutionNotFound),
			message:   "Execution does not exist.",
			retryable: false,
		},
		{
			name:      "execution limit reached",
			err:       domain.ErrExecutionLimitReached,
			status:    http.StatusTooManyRequests,
			code:      string(protocol.ErrorCodeExecutionLimitReached),
			message:   "Execution concurrency limit has been reached.",
			retryable: true,
		},
		{
			name:      "shell not found",
			err:       domain.ErrShellNotFound,
			status:    http.StatusUnprocessableEntity,
			code:      string(protocol.ErrorCodeShellNotFound),
			message:   "Requested shell is unavailable.",
			retryable: false,
		},
		{
			name:      "invalid cwd",
			err:       domain.ErrInvalidCWD,
			status:    http.StatusUnprocessableEntity,
			code:      string(protocol.ErrorCodeInvalidCWD),
			message:   "Execution working directory is invalid.",
			retryable: false,
		},
		{
			name:      "runner unhealthy",
			err:       domain.ErrRunnerUnhealthy,
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeRunnerUnhealthy),
			message:   "Sandbox runner is unavailable.",
			retryable: true,
		},
		{
			name:      "runner protocol mismatch",
			err:       domain.ErrRunnerProtocolMismatch,
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeRunnerProtocolMismatch),
			message:   "Sandbox runner protocol is incompatible.",
			retryable: false,
		},
		{
			name:      "outbound not allowed",
			err:       domain.ErrOutboundNotAllowed,
			status:    http.StatusForbidden,
			code:      string(protocol.ErrorCodeOutboundNotAllowed),
			message:   "Outbound network access is not allowed.",
			retryable: false,
		},
		{
			name:      "egress image unavailable",
			err:       domain.ErrEgressImageUnavailable,
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressImageUnavailable),
			message:   "Sandbox egress image is unavailable.",
			retryable: true,
		},
		{
			name:      "egress policy invalid",
			err:       fmt.Errorf("nft=%s: %w", secret, domain.ErrEgressPolicyInvalid),
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressPolicyInvalid),
			message:   "Sandbox egress policy is invalid.",
			retryable: false,
		},
		{
			name:      "egress not ready",
			err:       domain.ErrEgressNotReady,
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressNotReady),
			message:   "Sandbox egress is not ready.",
			retryable: true,
		},
		{
			name:      "egress unhealthy",
			err:       domain.ErrEgressUnhealthy,
			status:    http.StatusServiceUnavailable,
			code:      string(protocol.ErrorCodeEgressUnhealthy),
			message:   "Sandbox egress is unhealthy.",
			retryable: true,
		},
		{
			name:      "not found",
			err:       fmt.Errorf("lookup context: %w", domain.ErrNotFound),
			status:    http.StatusNotFound,
			code:      "SANDBOX_NOT_FOUND",
			message:   "Sandbox does not exist.",
			retryable: false,
		},
		{
			name:      "conflict",
			err:       fmt.Errorf("CAS context: %w", domain.ErrConflict),
			status:    http.StatusConflict,
			code:      "SANDBOX_CONFLICT",
			message:   "Request conflicts with the current sandbox state.",
			retryable: true,
		},
		{
			name:      "unknown",
			err:       errors.New(secret),
			status:    http.StatusInternalServerError,
			code:      "INTERNAL_ERROR",
			message:   "An unexpected internal error occurred.",
			retryable: true,
		},
		{
			name: "unavailable marker",
			err: &testUnavailableError{
				cause: errors.New(secret),
			},
			status:    http.StatusServiceUnavailable,
			code:      "RUNTIME_UNAVAILABLE",
			message:   "A required control-plane dependency is unavailable.",
			retryable: true,
		},
		{
			name:      "deadline",
			err:       context.DeadlineExceeded,
			status:    http.StatusServiceUnavailable,
			code:      "RUNTIME_UNAVAILABLE",
			message:   "A required control-plane dependency is unavailable.",
			retryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(requestIDHeader, "req-test")
			response := httptest.NewRecorder()

			writeError(response, request, tt.err)

			if response.Code != tt.status {
				t.Fatalf("status: got %d, want %d", response.Code, tt.status)
			}
			if response.Header().Get(requestIDHeader) != "req-test" {
				t.Fatalf("response request ID: %q", response.Header().Get(requestIDHeader))
			}
			var envelope protocol.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			want := protocol.ErrorDetail{
				Code:      tt.code,
				Message:   tt.message,
				RequestID: "req-test",
				Retryable: tt.retryable,
			}
			if envelope.Error != want {
				t.Fatalf("error detail: got %#v, want %#v", envelope.Error, want)
			}
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("response leaked internal cause: %s", response.Body.String())
			}
		})
	}
}

// TestWriteErrorUsesResponseRequestID 验证 middleware 生成的响应 ID 优先于请求头。
func TestWriteErrorUsesResponseRequestID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(requestIDHeader, "request-id")
	response := httptest.NewRecorder()
	response.Header().Set(requestIDHeader, "middleware-id")

	writeError(response, request, domain.ErrInvalid)

	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.RequestID != "middleware-id" {
		t.Fatalf("request ID: got %q, want middleware-id", envelope.Error.RequestID)
	}
}

// TestExecutionAndOutboundErrorsRedactInternalContext 验证专用错误不回显命令、环境或路径。
func TestExecutionAndOutboundErrorsRedactInternalContext(t *testing.T) {
	const sensitive = `argv=["sh","-c","secret"] env=TOKEN socket=/run/private.sock nft=10.0.0.0/8`
	errorsToMap := []error{
		domain.ErrInvalidExecutionRequest,
		domain.ErrSandboxNotRunning,
		domain.ErrExecutionNotFound,
		domain.ErrExecutionLimitReached,
		domain.ErrShellNotFound,
		domain.ErrInvalidCWD,
		domain.ErrRunnerUnhealthy,
		domain.ErrRunnerProtocolMismatch,
		domain.ErrOutboundNotAllowed,
		domain.ErrEgressImageUnavailable,
		domain.ErrEgressPolicyInvalid,
		domain.ErrEgressNotReady,
		domain.ErrEgressUnhealthy,
		domain.ErrInvalidTTL,
		domain.ErrInvalidExpiration,
		domain.ErrLeaseConflict,
		domain.ErrSandboxExpiring,
		domain.ErrIdempotencyConflict,
		domain.ErrSandboxLimitReached,
		domain.ErrAdminDisabled,
	}
	for _, mapped := range errorsToMap {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		writeError(response, request, fmt.Errorf("%s: %w", sensitive, mapped))
		if strings.Contains(response.Body.String(), sensitive) ||
			strings.Contains(response.Body.String(), "secret") ||
			strings.Contains(response.Body.String(), "/run/private.sock") {
			t.Fatalf("%v leaked sensitive context: %s", mapped, response.Body.String())
		}
	}
}
