package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

const renewTestSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

// TestRenewSandboxHandlerOK 验证 handler 传递绝对时间并返回公共 Sandbox。
func TestRenewSandboxHandlerOK(t *testing.T) {
	now := time.Date(2028, 8, 9, 10, 11, 12, 0, time.UTC)
	expiresAt := now.Add(2 * time.Hour)
	service := &fakeLifecycleService{renewResult: renewHTTPRecord(now, expiresAt)}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+renewTestSandboxID+"/renew", strings.NewReader(`{"expires_at":"2028-08-09T20:11:12+08:00"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Lifecycle: service}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.renewCalls) != 1 ||
		service.renewCalls[0].SandboxID != renewTestSandboxID || !service.renewCalls[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("renew response/call: status=%d calls=%#v body=%s", response.Code, service.renewCalls, response.Body.String())
	}
	var decoded protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil || !decoded.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("decode response: %#v/%v", decoded, err)
	}
}

// TestRenewSandboxHandlerRejectsInvalidRequests 验证格式、字段、大小、path 和 content type。
func TestRenewSandboxHandlerRejectsInvalidRequests(t *testing.T) {
	valid := `{"expires_at":"2028-08-09T10:11:12Z"}`
	tests := []struct {
		name, path, contentType string
		body                    []byte
	}{
		{name: "invalid path", path: "/v1/sandboxes/not-a-uuid/renew", contentType: "application/json", body: []byte(valid)},
		{name: "query", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew?force=true", contentType: "application/json", body: []byte(valid)},
		{name: "content type", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "text/plain", body: []byte(valid)},
		{name: "invalid time", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "application/json", body: []byte(`{"expires_at":"tomorrow"}`)},
		{name: "missing field", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "application/json", body: []byte(`{}`)},
		{name: "unknown field", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "application/json", body: []byte(`{"expires_at":"2028-08-09T10:11:12Z","ttl":60}`)},
		{name: "multiple values", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "application/json", body: []byte(valid + valid)},
		{name: "body limit", path: "/v1/sandboxes/" + renewTestSandboxID + "/renew", contentType: "application/json", body: bytes.Repeat([]byte("x"), int(maxRenewSandboxBodyBytes)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeLifecycleService{}
			request := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))
			request.Header.Set("Content-Type", tt.contentType)
			request.Header.Set(requestIDHeader, "renew-invalid")
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Lifecycle: service}).ServeHTTP(response, request)
			assertLifecycleError(t, response, http.StatusBadRequest, "INVALID_EXPIRATION", "renew-invalid")
			if len(service.renewCalls) != 0 {
				t.Fatalf("invalid request called service: %#v", service.renewCalls)
			}
		})
	}
}

// TestRenewSandboxHandlerMapsApplicationErrors 验证 404/409 及 request ID 统一映射且不泄露时间。
func TestRenewSandboxHandlerMapsApplicationErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   string
		status int
	}{
		{name: "not found", err: domain.ErrNotFound, code: "SANDBOX_NOT_FOUND", status: http.StatusNotFound},
		{name: "lease conflict", err: domain.ErrLeaseConflict, code: "LEASE_CONFLICT", status: http.StatusConflict},
		{name: "expiring", err: domain.ErrSandboxExpiring, code: "SANDBOX_EXPIRING", status: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeLifecycleService{renewErr: errors.Join(tt.err, errors.New("secret expiry 2099-01-01T00:00:00Z"))}
			request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+renewTestSandboxID+"/renew", strings.NewReader(`{"expires_at":"2028-08-09T10:11:12Z"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(requestIDHeader, "renew-error")
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Lifecycle: service}).ServeHTTP(response, request)
			assertLifecycleError(t, response, tt.status, tt.code, "renew-error")
			if strings.Contains(response.Body.String(), "2099") {
				t.Fatalf("error leaked expiry: %s", response.Body.String())
			}
		})
	}
}

// TestRenewSandboxRouteMethod 验证非 POST 请求由路由层返回 405。
func TestRenewSandboxRouteMethod(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "/v1/sandboxes/"+renewTestSandboxID+"/renew", nil)
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{}, RouterDependencies{Lifecycle: &fakeLifecycleService{}}).ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status: got %d, want 405", response.Code)
	}
}

func renewHTTPRecord(now, expiresAt time.Time) domain.Sandbox {
	return domain.Sandbox{
		ID: renewTestSandboxID, Spec: domain.SandboxSpec{Image: "alpine:3.22"},
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning,
		Reason: domain.SandboxReasonRunning, Message: "Sandbox is running.",
		ExpiresAt: &expiresAt, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
}
