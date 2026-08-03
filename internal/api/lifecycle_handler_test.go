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

	getResult domain.Sandbox
	getErr    error
	getCalls  []string

	deleteResult domain.Sandbox
	deleteErr    error
	deleteCalls  []application.DeleteSandbox
}

// Create 记录创建命令并返回测试预设结果。
func (f *fakeLifecycleService) Create(
	_ context.Context,
	command application.CreateSandbox,
) (domain.Sandbox, error) {
	f.createCalls = append(f.createCalls, command)
	return f.createResult, f.createErr
}

// Get 记录查询 ID 并返回测试预设结果。
func (f *fakeLifecycleService) Get(
	_ context.Context,
	id string,
) (domain.Sandbox, error) {
	f.getCalls = append(f.getCalls, id)
	return f.getResult, f.getErr
}

// Delete 记录删除命令并返回测试预设结果。
func (f *fakeLifecycleService) Delete(
	_ context.Context,
	command application.DeleteSandbox,
) (domain.Sandbox, error) {
	f.deleteCalls = append(f.deleteCalls, command)
	return f.deleteResult, f.deleteErr
}

// TestDeleteSandboxHandler 验证首次、处理中、已终止和不存在四类响应。
func TestDeleteSandboxHandler(t *testing.T) {
	const id = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	tests := []struct {
		name      string
		result    domain.Sandbox
		err       error
		status    int
		errorCode string
	}{
		{
			name: "first accepted",
			result: domain.Sandbox{
				ID:            id,
				DesiredState:  domain.DesiredTerminated,
				ObservedState: domain.StatePending,
			},
			status: http.StatusAccepted,
		},
		{
			name: "deletion in progress",
			result: domain.Sandbox{
				ID:            id,
				DesiredState:  domain.DesiredTerminated,
				ObservedState: domain.StateStopping,
			},
			status: http.StatusAccepted,
		},
		{
			name: "already terminated",
			result: domain.Sandbox{
				ID:            id,
				DesiredState:  domain.DesiredTerminated,
				ObservedState: domain.StateTerminated,
			},
			status: http.StatusNoContent,
		},
		{
			name:      "not found",
			err:       domain.ErrNotFound,
			status:    http.StatusNotFound,
			errorCode: "SANDBOX_NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeLifecycleService{
				deleteResult: tt.result,
				deleteErr:    tt.err,
			}
			request := httptest.NewRequest(
				http.MethodDelete,
				"/v1/sandboxes/"+id,
				nil,
			)
			request.Header.Set(requestIDHeader, "req-delete")
			response := httptest.NewRecorder()

			NewRouter(
				BuildInfo{Version: "test"},
				RouterDependencies{Lifecycle: service},
			).ServeHTTP(response, request)

			if response.Code != tt.status {
				t.Fatalf("status: got %d, want %d", response.Code, tt.status)
			}
			if got := response.Header().Get(requestIDHeader); got != "req-delete" {
				t.Fatalf("request ID: got %q, want req-delete", got)
			}
			if len(service.deleteCalls) != 1 ||
				service.deleteCalls[0].SandboxID != id {
				t.Fatalf("delete calls: %#v", service.deleteCalls)
			}
			if tt.errorCode != "" {
				var envelope protocol.ErrorResponse
				if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if envelope.Error.Code != tt.errorCode ||
					envelope.Error.RequestID != "req-delete" {
					t.Fatalf("error response: %#v", envelope)
				}
				return
			}
			if response.Body.Len() != 0 {
				t.Fatalf("success response body must be empty: %q", response.Body.String())
			}
		})
	}
}

// TestGetSandboxHandlerOK 验证查询成功响应使用公共 Sandbox mapper。
func TestGetSandboxHandlerOK(t *testing.T) {
	const id = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	now := time.Date(2027, 7, 8, 9, 10, 11, 0, time.UTC)
	service := &fakeLifecycleService{
		getResult: domain.Sandbox{
			ID:            id,
			Spec:          domain.SandboxSpec{Image: "alpine:3.22"},
			ObservedState: domain.StateRunning,
			Reason:        string(protocol.SandboxReasonRunning),
			Message:       "Sandbox is running.",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/sandboxes/"+id,
		nil,
	)
	response := httptest.NewRecorder()

	NewRouter(
		BuildInfo{Version: "test"},
		RouterDependencies{Lifecycle: service},
	).ServeHTTP(response, request)

	if got, want := response.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if len(service.getCalls) != 1 || service.getCalls[0] != id {
		t.Fatalf("get calls: %#v", service.getCalls)
	}
	var sandbox protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&sandbox); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sandbox.ID != id ||
		sandbox.State != protocol.SandboxStateRunning ||
		sandbox.Reason != protocol.SandboxReasonRunning {
		t.Fatalf("response sandbox: %#v", sandbox)
	}
}

// TestGetSandboxHandlerRejectsInvalidID 验证非法 ID 不进入应用层。
func TestGetSandboxHandlerRejectsInvalidID(t *testing.T) {
	service := &fakeLifecycleService{}
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/sandboxes/..%2Fhost",
		nil,
	)
	request.Header.Set(requestIDHeader, "req-invalid-id")
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
		"req-invalid-id",
	)
	if len(service.getCalls) != 0 {
		t.Fatalf("invalid ID called service: %#v", service.getCalls)
	}
}

// TestGetSandboxHandlerMapsServiceErrors 验证 404 和依赖不可用错误。
func TestGetSandboxHandlerMapsServiceErrors(t *testing.T) {
	const id = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			name:   "not found",
			err:    domain.ErrNotFound,
			status: http.StatusNotFound,
			code:   "SANDBOX_NOT_FOUND",
		},
		{
			name: "store unavailable",
			err: &testUnavailableError{
				cause: errors.New("secret sqlite path"),
			},
			status: http.StatusServiceUnavailable,
			code:   "RUNTIME_UNAVAILABLE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeLifecycleService{getErr: tt.err}
			request := httptest.NewRequest(
				http.MethodGet,
				"/v1/sandboxes/"+id,
				nil,
			)
			request.Header.Set(requestIDHeader, "req-get-error")
			response := httptest.NewRecorder()

			NewRouter(
				BuildInfo{Version: "test"},
				RouterDependencies{Lifecycle: service},
			).ServeHTTP(response, request)

			assertLifecycleError(
				t,
				response,
				tt.status,
				tt.code,
				"req-get-error",
			)
		})
	}
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

// TestCreateSandboxHandlerMapsOutbound 验证可选 public network 字段只映射布尔意图。
func TestCreateSandboxHandlerMapsOutbound(t *testing.T) {
	service := &fakeLifecycleService{
		createResult: domain.Sandbox{
			ID:            "00010203-0405-4607-8809-0a0b0c0d0e0f",
			Spec:          domain.SandboxSpec{Image: "alpine:3.22"},
			ObservedState: domain.StatePending,
			Reason:        string(protocol.SandboxReasonCreateAccepted),
			Message:       "Sandbox creation has been accepted.",
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"image":"alpine:3.22","network":{"outbound":true}}`),
	)
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Lifecycle: service}).
		ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d", response.Code, http.StatusAccepted)
	}
	if len(service.createCalls) != 1 || !service.createCalls[0].Outbound {
		t.Fatalf("outbound mapping: %#v", service.createCalls)
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
			name: "non boolean outbound",
			body: []byte(`{"image":"alpine:3.22","network":{"outbound":"yes"}}`),
		},
		{
			name: "unknown network field",
			body: []byte(`{"image":"alpine:3.22","network":{"cidr":"0.0.0.0/0"}}`),
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
