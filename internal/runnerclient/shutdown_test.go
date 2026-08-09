package runnerclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientShutdownUsesFixedEndpoint 验证关闭操作不经过 health gate 且只调用固定路径。
func TestClientShutdownUsesFixedEndpoint(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &Client{httpClient: server.Client(), baseURL: server.URL, authorization: func() ([]byte, error) { return []byte("token"), nil }}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if method != http.MethodPost || path != "/v1/shutdown" {
		t.Fatalf("request: %s %s", method, path)
	}
}
