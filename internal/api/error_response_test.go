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

// TestExecutionErrorsRedactInternalContext 验证execution 错误不回显命令、环境或路径。
func TestExecutionErrorsRedactInternalContext(t *testing.T) {
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
