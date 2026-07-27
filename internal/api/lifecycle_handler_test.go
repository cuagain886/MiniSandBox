package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// fakeLifecycleService 记录 HTTP adapter 向应用层提交的参数并返回预设结果。
type fakeLifecycleService struct {
	createResult domain.Sandbox
	createErr    error
	createCalls  []application.CreateSandbox
}

// Create 记录创建命令并返回测试预设结果。
func (f *fakeLifecycleService) Create(
	_ context.Context,
	command application.CreateSandbox,
) (domain.Sandbox, error) {
	f.createCalls = append(f.createCalls, command)
	return f.createResult, f.createErr
}

// Get 在 P1-031 测试中不应被调用。
func (f *fakeLifecycleService) Get(
	context.Context,
	string,
) (domain.Sandbox, error) {
	panic("unexpected Get call")
}

// Delete 在 P1-031 测试中不应被调用。
func (f *fakeLifecycleService) Delete(
	context.Context,
	application.DeleteSandbox,
) (domain.Sandbox, error) {
	panic("unexpected Delete call")
}

// TestCreateSandboxHandlerAccepted 验证创建请求只提交意图并返回 202 与 Location。
func TestCreateSandboxHandlerAccepted(t *testing.T) {
	now := time.Date(2027, 7, 8, 9, 10, 11, 0, time.UTC)
	service := &fakeLifecycleService{
		createResult: domain.Sandbox{
			ID:            "00010203-0405-4607-8809-0a0b0c0d0e0f",
			Spec:          domain.SandboxSpec{Image: "alpine:3.22"},
			ObservedState: domain.StatePending,
			Reason:        string(protocol.SandboxReasonCreateAccepted),
			Message:       "Sandbox creation has been accepted.",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"image":"alpine:3.22"}`),
	)
	request.Header.Set(requestIDHeader, "req-create")
	response := httptest.NewRecorder()

	NewRouter(
		BuildInfo{Version: "test"},
		RouterDependencies{Lifecycle: service},
	).ServeHTTP(response, request)

	if got, want := response.Code, http.StatusAccepted; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if got, want := response.Header().Get("Location"),
		"/v1/sandboxes/00010203-0405-4607-8809-0a0b0c0d0e0f"; got != want {
		t.Fatalf("Location: got %q, want %q", got, want)
	}
	if len(service.createCalls) != 1 ||
		service.createCalls[0].Image != "alpine:3.22" {
		t.Fatalf("create calls: %#v", service.createCalls)
	}
	var sandbox protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&sandbox); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sandbox.ID != service.createResult.ID ||
		sandbox.State != protocol.SandboxStatePending {
		t.Fatalf("response sandbox: %#v", sandbox)
	}
}

// TestCreateSandboxHandlerRejectsInvalidBodies 验证严格 JSON 和 body 上限。
func TestCreateSandboxHandlerRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid JSON", body: []byte(`{"image":`)},
		{
			name: "unknown field",
			body: []byte(`{"image":"alpine:3.22","privileged":true}`),
		},
		{
			name: "multiple documents",
			body: []byte(`{"image":"alpine:3.22"} {"image":"busybox"}`),
		},
		{
			name: "body limit",
			body: bytes.Repeat([]byte("x"), int(maxCreateSandboxBodyBytes)+1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeLifecycleService{}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/sandboxes",
				bytes.NewReader(tt.body),
			)
			request.Header.Set(requestIDHeader, "req-invalid")
			response := httptest.NewRecorder()

			NewRouter(
				BuildInfo{Version: "test"},
				RouterDependencies{Lifecycle: service},
			).ServeHTTP(response, request)

			assertLifecycleError(
				t,
				response,
				http.StatusBadRequest,
				"INVALID_REQUEST",
				"req-invalid",
			)
			if len(service.createCalls) != 0 {
				t.Fatalf("invalid request called service: %#v", service.createCalls)
			}
		})
	}
}

// TestCreateSandboxHandlerMapsServiceError 验证应用错误经过统一 mapper。
func TestCreateSandboxHandlerMapsServiceError(t *testing.T) {
	service := &fakeLifecycleService{
		createErr: errors.New("secret internal store path"),
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"image":"alpine:3.22"}`),
	)
	request.Header.Set(requestIDHeader, "req-error")
	response := httptest.NewRecorder()

	NewRouter(
		BuildInfo{Version: "test"},
		RouterDependencies{Lifecycle: service},
	).ServeHTTP(response, request)

	assertLifecycleError(
		t,
		response,
		http.StatusInternalServerError,
		"INTERNAL_ERROR",
		"req-error",
	)
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("response leaked service error: %s", response.Body.String())
	}
}

// assertLifecycleError 校验生命周期 handler 使用统一错误 envelope。
func assertLifecycleError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
	requestID string,
) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status: got %d, want %d", response.Code, status)
	}
	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if envelope.Error.Code != code ||
		envelope.Error.RequestID != requestID {
		t.Fatalf("error response: %#v", envelope)
	}
}
