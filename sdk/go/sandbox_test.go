package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestCreateSandboxRequestMapping 验证 SDK 只发送 Phase 1 支持的创建字段和路径。
func TestCreateSandboxRequestMapping(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if got, want := request.Method, http.MethodPost; got != want {
			t.Errorf("unexpected method: got %s, want %s", got, want)
		}
		if got, want := request.URL.Path, "/v1/sandboxes"; got != want {
			t.Errorf("unexpected path: got %s, want %s", got, want)
		}
		if got, want := request.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("unexpected Content-Type: got %q, want %q", got, want)
		}
		if value := request.Header.Get("Idempotency-Key"); value != "" {
			t.Errorf("unexpected Idempotency-Key header: %q", value)
		}

		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(response, "invalid request", http.StatusBadRequest)
			return
		}
		if got, want := len(body), 1; got != want {
			t.Errorf("unexpected request field count: got %d, want %d", got, want)
		}
		if got := string(body["image"]); got != `"alpine:3.22"` {
			t.Errorf("unexpected image field: %s", got)
		}

		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(protocol.Sandbox{
			ID:        "sbx-test",
			State:     protocol.SandboxStatePending,
			Reason:    protocol.SandboxReasonCreateAccepted,
			Message:   "Sandbox creation has been accepted.",
			Image:     "alpine:3.22",
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.CreateSandbox(
		context.Background(),
		protocol.CreateSandboxRequest{Image: "alpine:3.22"},
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if got, want := sandbox.ID, "sbx-test"; got != want {
		t.Fatalf("unexpected sandbox ID: got %s, want %s", got, want)
	}
	if !sandbox.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected created time: got %s, want %s", sandbox.CreatedAt, createdAt)
	}
	if got, want := sandbox.Reason, protocol.SandboxReasonCreateAccepted; got != want {
		t.Fatalf("unexpected sandbox reason: got %s, want %s", got, want)
	}
	if got, want := sandbox.Message, "Sandbox creation has been accepted."; got != want {
		t.Fatalf("unexpected sandbox message: got %q, want %q", got, want)
	}
	if !sandbox.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected updated time: got %s, want %s", sandbox.UpdatedAt, updatedAt)
	}
}

// TestGetSandboxResponseMapping 验证查询接口复用与创建接口相同的 Sandbox 响应模型。
func TestGetSandboxResponseMapping(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if got, want := request.Method, http.MethodGet; got != want {
			t.Errorf("unexpected method: got %s, want %s", got, want)
		}
		if got, want := request.URL.Path, "/v1/sandboxes/sbx-test"; got != want {
			t.Errorf("unexpected path: got %s, want %s", got, want)
		}

		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(protocol.Sandbox{
			ID:        "sbx-test",
			State:     protocol.SandboxStateRunning,
			Reason:    protocol.SandboxReasonRunning,
			Message:   "Sandbox is running.",
			Image:     "alpine:3.22",
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.GetSandbox(context.Background(), "sbx-test")
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got, want := sandbox.State, protocol.SandboxStateRunning; got != want {
		t.Fatalf("unexpected sandbox state: got %s, want %s", got, want)
	}
	if got, want := sandbox.Reason, protocol.SandboxReasonRunning; got != want {
		t.Fatalf("unexpected sandbox reason: got %s, want %s", got, want)
	}
	if !sandbox.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected updated time: got %s, want %s", sandbox.UpdatedAt, updatedAt)
	}
}
