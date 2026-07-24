package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
