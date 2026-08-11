package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// TestMapIdempotencyKey 验证缺失、单值、重复和代理逗号合并语义。
func TestMapIdempotencyKey(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		key, err := mapIdempotencyKey(make(http.Header))
		if err != nil || key != nil {
			t.Fatalf("absent key: %#v/%v", key, err)
		}
	})
	t.Run("single", func(t *testing.T) {
		header := make(http.Header)
		header.Set("Idempotency-Key", "request_1:retry")
		key, err := mapIdempotencyKey(header)
		if err != nil || key == nil || key.ScopeID() != "local:v1" || key.Value() != "request_1:retry" {
			t.Fatalf("single key: %#v/%v", key, err)
		}
	})
	for _, testCase := range []struct {
		name   string
		values []string
	}{
		{"empty", []string{""}},
		{"duplicate field", []string{"first", "second"}},
		{"comma joined", []string{"first, second"}},
		{"invalid character", []string{"secret value"}},
		{"too long", []string{strings.Repeat("k", 129)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			header := make(http.Header)
			header[http.CanonicalHeaderKey("Idempotency-Key")] = testCase.values
			key, err := mapIdempotencyKey(header)
			if !errors.Is(err, domain.ErrInvalid) || key != nil {
				t.Fatalf("got %#v/%v, want nil ErrInvalid", key, err)
			}
			for _, value := range testCase.values {
				if value != "" && strings.Contains(err.Error(), value) {
					t.Fatal("error leaked raw header value")
				}
			}
		})
	}
}

// TestCreateHandlerScopesAndRedactsIdempotencyKey 验证 handler 传递 scoped key 且错误响应不回显。
func TestCreateHandlerScopesAndRedactsIdempotencyKey(t *testing.T) {
	now := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := now.Add(time.Hour)
	service := &fakeLifecycleService{createResult: domain.Sandbox{
		ID: "00010203-0405-4607-8809-0a0b0c0d0e0f", Spec: domain.SandboxSpec{Image: "alpine"},
		ObservedState: domain.StatePending, Reason: domain.SandboxReasonCreateAccepted,
		Message: "Sandbox creation has been accepted.", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires,
	}}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(`{"image":"alpine"}`))
	request.Header.Set("Idempotency-Key", "safe.key:1")
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Lifecycle: service}).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(service.createCalls) != 1 ||
		service.createCalls[0].Idempotency == nil ||
		service.createCalls[0].Idempotency.ScopeID() != "local:v1" ||
		service.createCalls[0].Idempotency.Value() != "safe.key:1" {
		t.Fatalf("scoped create: status=%d calls=%#v", response.Code, service.createCalls)
	}

	const sentinel = "secret-key,other"
	invalid := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(`{"image":"alpine"}`))
	invalid.Header.Set("Idempotency-Key", sentinel)
	invalidResponse := httptest.NewRecorder()
	NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Lifecycle: service}).ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || len(service.createCalls) != 1 ||
		strings.Contains(invalidResponse.Body.String(), sentinel) {
		t.Fatalf("invalid key response: status=%d body=%q calls=%d", invalidResponse.Code, invalidResponse.Body.String(), len(service.createCalls))
	}
}
