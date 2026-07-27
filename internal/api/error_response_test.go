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
