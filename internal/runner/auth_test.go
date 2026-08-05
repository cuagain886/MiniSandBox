package runner

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTokenAuthAcceptsExactlyOneCorrectHeader 验证所有受保护路径只接受一个完全匹配的
// Bearer header，并对缺失、错误和重复凭据返回相同的 401。
func TestTokenAuthAcceptsExactlyOneCorrectHeader(t *testing.T) {
	const token = "credential-secret-canary"
	called := 0
	handler, err := TokenAuth(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatalf("build auth middleware: %v", err)
	}
	tests := []struct {
		name    string
		headers []string
		status  int
	}{
		{name: "correct", headers: []string{"Bearer " + token}, status: http.StatusNoContent},
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", headers: []string{"Bearer wrong"}, status: http.StatusUnauthorized},
		{name: "wrong scheme", headers: []string{"bearer " + token}, status: http.StatusUnauthorized},
		{name: "duplicate", headers: []string{"Bearer " + token, "Bearer " + token}, status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			for _, value := range test.headers {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status: got %d, want %d", response.Code, test.status)
			}
			if strings.Contains(response.Body.String(), token) {
				t.Fatal("credential leaked into authentication response")
			}
		})
	}
	if called != 1 {
		t.Fatalf("next handler calls: got %d, want 1", called)
	}
}

// TestTokenAuthRejectsUnsafeConfiguration 验证空 token 或空下游 handler 不能构造服务。
func TestTokenAuthRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := TokenAuth("", http.NotFoundHandler()); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := TokenAuth("token", nil); err == nil {
		t.Fatal("nil handler accepted")
	}
}

// TestServerProtectsHealthAndExecutionRoutes 验证 health 和 execution 路由共享强制鉴权边界。
func TestServerProtectsHealthAndExecutionRoutes(t *testing.T) {
	handler, err := newServer("test-build", "test-token", func() (string, error) {
		return "linux-netns:4:99", nil
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodPost, "/v1/executions"},
		{http.MethodDelete, "/v1/executions/exec_1"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(target.method, target.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d, want 401", target.method, target.path, response.Code)
		}
	}
	if _, err := NewServer("test-build", ""); err == nil {
		t.Fatal("server accepted empty token")
	}
}
