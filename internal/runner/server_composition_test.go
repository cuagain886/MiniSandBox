package runner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

func TestConfiguredServerRequiresReadinessAndExactRoutes(t *testing.T) {
	routes := ServerRoutes{}
	if _, err := newConfiguredServer("build", "token", NewServerReadiness(), routes, func() (string, error) { return "netns", nil }); err == nil {
		t.Fatal("incomplete route table accepted")
	}

	called := make(chan string, 5)
	handler := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called <- name
			w.WriteHeader(http.StatusNoContent)
		})
	}
	readiness := NewServerReadiness()
	server, err := newConfiguredServer("build", "token", readiness, ServerRoutes{
		Create: handler("create"), Status: handler("status"), Cancel: handler("cancel"), Logs: handler("logs"), Shutdown: handler("shutdown"),
		Capabilities: NewCapabilitiesHandler(protocol.Capabilities{}),
	}, func() (string, error) { return "linux-netns:4:9", nil })
	if err != nil {
		t.Fatalf("new configured server: %v", err)
	}

	assertHealthStatus(t, server, "starting", http.StatusServiceUnavailable)
	if err := readiness.MarkReady(); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	assertHealthStatus(t, server, "ok", http.StatusOK)

	for _, test := range []struct{ method, path, want string }{
		{http.MethodPost, "/v1/executions", "create"},
		{http.MethodGet, "/v1/executions/exec_1", "status"},
		{http.MethodDelete, "/v1/executions/exec_1", "cancel"},
		{http.MethodGet, "/v1/executions/exec_1/logs", "logs"},
		{http.MethodPost, "/v1/shutdown", "shutdown"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || <-called != test.want {
			t.Fatalf("route %s %s did not select %s", test.method, test.path, test.want)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/executions/exec_1/extra", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("catch-all route exists: status %d", response.Code)
	}

	readiness.StartDraining()
	assertHealthStatus(t, server, "draining", http.StatusServiceUnavailable)
	if err := readiness.MarkReady(); err == nil {
		t.Fatal("draining readiness became ready again")
	}
}

func TestServeManagedDrainsBeforeHTTPShutdown(t *testing.T) {
	manager, err := NewManager(1)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	readiness := NewServerReadiness()
	if err := readiness.MarkReady(); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeManaged(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), manager, readiness)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve managed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("managed server leaked during shutdown")
	}
	if !readiness.draining.Load() {
		t.Fatal("server did not enter draining")
	}
	if _, err := manager.CreateExecution(); err != ErrRunnerShuttingDown {
		t.Fatalf("new execution after shutdown: %v", err)
	}
}

func assertHealthStatus(t *testing.T, handler http.Handler, want string, wantCode int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantCode {
		t.Fatalf("health status code: got %d, want %d", response.Code, wantCode)
	}
	var health protocol.RunnerHealth
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.Status != want || health.Version != "build" {
		t.Fatalf("health: %+v", health)
	}
}
