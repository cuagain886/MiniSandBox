package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestClientDecodesErrorResponse 验证 SDK 保留公共错误码、请求标识和重试语义。
func TestClientDecodesErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:      "RUNTIME_UNAVAILABLE",
				Message:   "Sandbox runtime is unavailable.",
				RequestID: "req-test",
				Retryable: true,
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	_, err := client.GetSandbox(context.Background(), "sbx-test")
	if err == nil {
		t.Fatal("expected an error")
	}
	var responseError *ResponseError
	if !errors.As(err, &responseError) {
		t.Fatalf("expected ResponseError, got %T: %v", err, err)
	}
	if got, want := responseError.StatusCode, http.StatusServiceUnavailable; got != want {
		t.Fatalf("unexpected status: got %d, want %d", got, want)
	}
	if got, want := responseError.Detail.RequestID, "req-test"; got != want {
		t.Fatalf("unexpected request ID: got %s, want %s", got, want)
	}
	if !responseError.Detail.Retryable {
		t.Fatal("expected retryable error")
	}
}
