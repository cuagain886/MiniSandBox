package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestHealth 验证控制面健康检查和请求 ID 中间件的最小契约。
func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	NewRouter(BuildInfo{Version: "test"}).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if response.Header().Get(requestIDHeader) == "" {
		t.Fatal("expected a generated request ID")
	}
}

// TestNotImplementedErrorResponse 验证占位 handler 也遵循统一公共错误 envelope。
func TestNotImplementedErrorResponse(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", nil)
	request.Header.Set(requestIDHeader, "req-test")
	response := httptest.NewRecorder()

	NewRouter(BuildInfo{Version: "test"}).ServeHTTP(response, request)

	if got, want := response.Code, http.StatusNotImplemented; got != want {
		t.Fatalf("unexpected status: got %d, want %d", got, want)
	}
	if got, want := response.Header().Get(requestIDHeader), "req-test"; got != want {
		t.Fatalf("unexpected response request ID: got %s, want %s", got, want)
	}
	if got, want := response.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("unexpected Content-Type: got %q, want %q", got, want)
	}
	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got, want := envelope.Error.Code, "NOT_IMPLEMENTED"; got != want {
		t.Fatalf("unexpected error code: got %s, want %s", got, want)
	}
	if got, want := envelope.Error.RequestID, "req-test"; got != want {
		t.Fatalf("unexpected request ID: got %s, want %s", got, want)
	}
	if envelope.Error.Retryable {
		t.Fatal("not implemented response must not be retryable")
	}
}
