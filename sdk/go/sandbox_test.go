package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestCreateSandboxRequestMapping 验证 SDK 发送 image 和可选 outbound 字段。
func TestCreateSandboxRequestMapping(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	expiresAt := createdAt.Add(time.Hour)
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
		if got, want := len(body), 2; got != want {
			t.Errorf("unexpected request field count: got %d, want %d", got, want)
		}
		if got := string(body["image"]); got != `"alpine:3.22"` {
			t.Errorf("unexpected image field: %s", got)
		}
		if got := string(body["network"]); got != `{"outbound":true}` {
			t.Errorf("unexpected network field: %s", got)
		}

		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(protocol.Sandbox{
			ID:        "sbx-test",
			State:     protocol.SandboxStatePending,
			Reason:    protocol.SandboxReasonCreateAccepted,
			Message:   "Sandbox creation has been accepted.",
			Image:     "alpine:3.22",
			ExpiresAt: expiresAt,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.CreateSandbox(
		context.Background(),
		protocol.CreateSandboxRequest{
			Image: "alpine:3.22",
			Network: &protocol.SandboxNetworkRequest{
				Outbound: true,
			},
		},
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
	if !sandbox.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected expiration time: got %s, want %s", sandbox.ExpiresAt, expiresAt)
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

// TestCreateSandboxWithOptionsMapping 验证 SDK 发送单个幂等 header 和原生 TTL 映射。
func TestCreateSandboxWithOptionsMapping(t *testing.T) {
	ttl := time.Hour
	expiresAt := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		values := request.Header.Values(idempotencyKeyHeader)
		if len(values) != 1 || values[0] != "build-job_42:v1" {
			t.Errorf("unexpected idempotency header values: %#v", values)
		}
		var body protocol.CreateSandboxRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body.TTLSeconds == nil || *body.TTLSeconds != 3600 {
			t.Errorf("unexpected TTL mapping: %#v", body.TTLSeconds)
		}
		if body.Network == nil || !body.Network.Outbound {
			t.Errorf("unexpected network mapping: %#v", body.Network)
		}

		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Location", "/v1/sandboxes/sbx-idempotent")
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(protocol.Sandbox{
			ID:        "sbx-idempotent",
			State:     protocol.SandboxStatePending,
			Reason:    protocol.SandboxReasonCreateAccepted,
			Message:   "Sandbox creation has been accepted.",
			Image:     "alpine:3.22",
			ExpiresAt: expiresAt,
			CreatedAt: expiresAt.Add(-time.Hour),
			UpdatedAt: expiresAt.Add(-time.Hour),
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.CreateSandboxWithOptions(
		context.Background(),
		CreateSandboxRequest{
			Image: "alpine:3.22",
			TTL:   &ttl,
			Network: &SandboxNetworkRequest{
				Outbound: true,
			},
		},
		CreateSandboxOptions{IdempotencyKey: "build-job_42:v1"},
	)
	if err != nil {
		t.Fatalf("create sandbox with options: %v", err)
	}
	if sandbox.ID != "sbx-idempotent" || !sandbox.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected sandbox response: %#v", sandbox)
	}
}

// TestCreateSandboxOptionsIdempotencyKey 验证 key 字符、长度边界和安全错误文本。
func TestCreateSandboxOptionsIdempotencyKey(t *testing.T) {
	valid := []string{
		"a",
		strings.Repeat("A", 128),
		"Az09._:-",
	}
	for _, value := range valid {
		if !validIdempotencyKey(value) {
			t.Errorf("valid key was rejected: length=%d", len(value))
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 129),
		"has space",
		"slash/value",
		"comma,value",
		"unicode-密钥",
		"line\nbreak",
	}
	for _, value := range invalid {
		if validIdempotencyKey(value) {
			t.Errorf("invalid key was accepted: length=%d", len(value))
		}
	}

	secretInvalid := "secret/value"
	client := NewClient("http://127.0.0.1:1", nil)
	_, err := client.CreateSandboxWithOptions(
		context.Background(),
		CreateSandboxRequest{Image: "alpine:3.22"},
		CreateSandboxOptions{IdempotencyKey: secretInvalid},
	)
	if err == nil {
		t.Fatal("invalid idempotency key must be rejected before HTTP")
	}
	if strings.Contains(err.Error(), secretInvalid) {
		t.Fatalf("validation error leaked idempotency key: %v", err)
	}
}

// TestRenewSandboxMapping 验证 SDK 使用公开 renew path、UTC 时间和公共响应模型。
func TestRenewSandboxMapping(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	requested := time.Date(2026, time.July, 25, 21, 0, 0, 123_456_789, location)
	createdAt := time.Date(2026, time.July, 25, 11, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if got, want := request.Method, http.MethodPost; got != want {
			t.Errorf("unexpected method: got %s, want %s", got, want)
		}
		if got, want := request.URL.Path, "/v1/sandboxes/sbx-test/renew"; got != want {
			t.Errorf("unexpected path: got %s, want %s", got, want)
		}
		content, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		const wantBody = `{"expires_at":"2026-07-25T13:00:00.123456789Z"}` + "\n"
		if !bytes.Equal(content, []byte(wantBody)) {
			t.Errorf("unexpected request body: got %s, want %s", content, wantBody)
		}

		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(protocol.Sandbox{
			ID:        "sbx-test",
			State:     protocol.SandboxStateRunning,
			Reason:    protocol.SandboxReasonRunning,
			Message:   "Sandbox is running.",
			Image:     "alpine:3.22",
			ExpiresAt: requested.UTC(),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.RenewSandbox(
		context.Background(),
		"sbx-test",
		RenewSandboxRequest{ExpiresAt: requested},
	)
	if err != nil {
		t.Fatalf("renew sandbox: %v", err)
	}
	if !sandbox.ExpiresAt.Equal(requested) || sandbox.ExpiresAt.Location() != time.UTC {
		t.Fatalf("renewed expiration: got %s, want %s UTC", sandbox.ExpiresAt, requested)
	}
}

// TestRenewSandboxRejectsZeroExpiration 验证 SDK 不发送无法表示租约的零时间。
func TestRenewSandboxRejectsZeroExpiration(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", nil)
	if _, err := client.RenewSandbox(
		context.Background(),
		"sbx-test",
		RenewSandboxRequest{},
	); err == nil {
		t.Fatal("zero expiration must be rejected before HTTP")
	}
}

// TestGetSandboxResponseMapping 验证查询接口复用与创建接口相同的 Sandbox 响应模型。
func TestGetSandboxResponseMapping(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	expiresAt := createdAt.Add(time.Hour)
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
			ExpiresAt: expiresAt,
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
	if !sandbox.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected expiration time: got %s, want %s", sandbox.ExpiresAt, expiresAt)
	}
}

// TestCreateSandboxRequestWireTTLMapping 验证 SDK 只接受协议边界内的整秒 TTL。
func TestCreateSandboxRequestWireTTLMapping(t *testing.T) {
	tests := []struct {
		name    string
		ttl     *time.Duration
		want    int64
		wantErr bool
	}{
		{name: "omitted"},
		{name: "minimum", ttl: durationPointer(time.Minute), want: 60},
		{name: "maximum", ttl: durationPointer(24 * time.Hour), want: 86_400},
		{name: "zero", ttl: durationPointer(0), wantErr: true},
		{name: "negative", ttl: durationPointer(-time.Second), wantErr: true},
		{name: "below minimum", ttl: durationPointer(time.Minute - time.Second), wantErr: true},
		{name: "above maximum", ttl: durationPointer(24*time.Hour + time.Second), wantErr: true},
		{name: "fractional second", ttl: durationPointer(time.Minute + time.Millisecond), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := (CreateSandboxRequest{
				Image: "alpine:3.22",
				TTL:   test.ttl,
				Network: &SandboxNetworkRequest{
					Outbound: true,
				},
			}).wire()
			if test.wantErr {
				if err == nil {
					t.Fatal("expected TTL mapping error")
				}
				return
			}
			if err != nil {
				t.Fatalf("map TTL: %v", err)
			}
			if wire.Network == nil || !wire.Network.Outbound {
				t.Fatalf("network mapping: %#v", wire.Network)
			}
			if test.ttl == nil {
				if wire.TTLSeconds != nil {
					t.Fatalf("omitted TTL mapped to %#v", wire.TTLSeconds)
				}
				return
			}
			if wire.TTLSeconds == nil || *wire.TTLSeconds != test.want {
				t.Fatalf("TTL seconds: got %#v, want %d", wire.TTLSeconds, test.want)
			}
		})
	}
}

func durationPointer(value time.Duration) *time.Duration { return &value }
