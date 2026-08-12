package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"minisandbox/internal/application"
)

type diagnosticsServiceFake struct{}

func (diagnosticsServiceFake) Snapshot(context.Context) application.DiagnosticsSnapshot {
	return application.DiagnosticsSnapshot{
		Store:     application.DiagnosticsSection{Status: "available"},
		Runtime:   application.DiagnosticsSection{Status: "unavailable"},
		Runner:    application.DiagnosticsSection{Status: "unavailable"},
		Scheduler: application.DiagnosticsSection{Status: "available"},
		Anomalies: application.DiagnosticsSection{Status: "available"},
	}
}

// TestAdminRoutesAreAbsentUnlessExplicitlyWired 验证 admin 关闭时不暴露相似占位路由。
func TestAdminRoutesAreAbsentUnlessExplicitlyWired(t *testing.T) {
	router := NewRouter(BuildInfo{Version: "test"})
	for _, path := range []string{"/metrics", "/v1/admin/diagnostics"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("disabled route %s: got %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}

// TestAdminRoutesUseExactGETPatternsAndSharedRequestID 验证只注册两个精确 GET 路由并复用根中间件请求 ID。
func TestAdminRoutesUseExactGETPatternsAndSharedRequestID(t *testing.T) {
	diagnostics := NewDiagnosticsHandler(diagnosticsServiceFake{})
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	router := NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Metrics: metrics, Diagnostics: diagnostics})

	request := httptest.NewRequest(http.MethodGet, "/v1/admin/diagnostics", nil)
	request.Header.Set(requestIDHeader, "req-admin-test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get(requestIDHeader) != "req-admin-test" {
		t.Fatalf("diagnostics response: code=%d request_id=%q", response.Code, response.Header().Get(requestIDHeader))
	}
	var snapshot application.DiagnosticsSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil || snapshot.Store.Status != "available" {
		t.Fatalf("diagnostics JSON: snapshot=%+v err=%v", snapshot, err)
	}

	for _, test := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/metrics", http.StatusOK},
		{http.MethodPost, "/metrics", http.StatusMethodNotAllowed},
		{http.MethodGet, "/metrics/extra", http.StatusNotFound},
		{http.MethodPost, "/v1/admin/diagnostics", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/admin/diagnostics/extra", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s: got %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}
