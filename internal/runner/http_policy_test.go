package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestRunnerRequestPolicyRejectsOversizedHeaderAndPath 验证 header/path 在路由前被拒绝并返回统一 envelope。
func TestRunnerRequestPolicyRejectsOversizedHeaderAndPath(t *testing.T) {
	called := false
	policy, err := RunnerRequestPolicy(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	tests := []struct {
		name   string
		path   string
		header string
		want   int
	}{
		{name: "header", path: "/healthz", header: strings.Repeat("secret-canary", runnerMaxHeaderBytes), want: http.StatusRequestHeaderFieldsTooLarge},
		{name: "path", path: "/" + strings.Repeat("x", runnerMaxPathBytes), want: http.StatusRequestURITooLong},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.header != "" {
				request.Header.Set("X-Oversized", test.header)
			}
			response := httptest.NewRecorder()
			policy.ServeHTTP(response, request)
			if response.Code != test.want || response.Header().Get("Content-Type") != "application/json" || response.Header().Get(runnerRequestIDHeader) == "" {
				t.Fatalf("response: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var envelope protocol.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.RequestID == "" || strings.Contains(response.Body.String(), "secret-canary") {
				t.Fatalf("envelope=%+v err=%v", envelope, err)
			}
		})
	}
	if called {
		t.Fatal("oversized request reached route")
	}
}

// TestRunnerNonSSEErrorsUseContractEnvelope 验证鉴权、health 内部失败和占位路由都使用统一错误模型。
func TestRunnerNonSSEErrorsUseContractEnvelope(t *testing.T) {
	handler, err := newServer("test", "token", func() (string, error) { return "", http.ErrServerClosed })
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	tests := []struct {
		name string
		path string
		auth bool
		want int
	}{
		{name: "auth", path: "/healthz", want: http.StatusUnauthorized},
		{name: "health", path: "/healthz", auth: true, want: http.StatusServiceUnavailable},
		{name: "route", path: "/v1/executions", auth: true, want: http.StatusNotImplemented},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(map[bool]string{false: http.MethodGet, true: http.MethodPost}[test.name == "route"], test.path, nil)
			if test.auth {
				request.Header.Set("Authorization", "Bearer token")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var envelope protocol.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Code == "" || envelope.Error.RequestID == "" {
				t.Fatalf("envelope=%+v err=%v", envelope, err)
			}
		})
	}
}

// TestRunnerHTTPServerHasFiniteReadAndIdleLimits 验证 slow header/body 与空闲连接由 server 层有限时间约束。
func TestRunnerHTTPServerHasFiniteReadAndIdleLimits(t *testing.T) {
	server := newRunnerHTTPServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout != runnerHeaderTimeout || server.ReadTimeout != runnerReadTimeout || server.IdleTimeout != runnerIdleTimeout || server.MaxHeaderBytes != runnerMaxHeaderBytes || server.WriteTimeout != 0 {
		t.Fatalf("server limits: %+v", server)
	}
}
