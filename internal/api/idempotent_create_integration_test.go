package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/config"
	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/internal/testutil"
	"minisandbox/pkg/protocol"
)

type sequenceCreateIDGenerator struct{ next int }

func (g *sequenceCreateIDGenerator) NewID() (string, error) {
	g.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", g.next), nil
}

type fixedCreateClock struct{ now time.Time }

func (c fixedCreateClock) Now() time.Time { return c.now }

// TestIdempotentCreatePublicPath 验证公共 API 的新建、重放、冲突、无 key、quota 和 request ID。
func TestIdempotentCreatePublicPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandboxd.db")
	store, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	cfg := config.Default()
	waker := testutil.NewFakeWaker()
	service := application.NewSandboxServiceWithCreatePolicy(
		store, &sequenceCreateIDGenerator{}, fixedCreateClock{now: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)},
		application.NewSandboxSpecBuilder(cfg.DefaultSandboxSpec(), cfg.Limits.MaxResources),
		waker, false,
		application.CreatePolicy{
			DefaultTTL: 30 * time.Minute, MinimumTTL: time.Minute,
			MaximumTTL: 24 * time.Hour, MaxSandboxes: 3,
		},
	)
	router := NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Lifecycle: service})

	first := performCreateRequest(t, router, `{"image":"alpine:3.22","ttl_seconds":3600}`, "same-key")
	if first.Code != http.StatusAccepted || first.Header().Get("Location") == "" {
		t.Fatalf("first create: status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}
	replay := performCreateRequest(t, router, `{"image":"alpine:3.22","ttl_seconds":3600}`, "same-key")
	if replay.Code != http.StatusAccepted || replay.Header().Get("Location") != first.Header().Get("Location") ||
		!bytes.Equal(replay.Body.Bytes(), first.Body.Bytes()) {
		t.Fatalf("replay differs: first=%s replay=%s", first.Body.String(), replay.Body.String())
	}
	if first.Header().Get(requestIDHeader) == "" || first.Header().Get(requestIDHeader) == replay.Header().Get(requestIDHeader) {
		t.Fatalf("request IDs are not per attempt: %q/%q", first.Header().Get(requestIDHeader), replay.Header().Get(requestIDHeader))
	}

	conflict := performCreateRequest(t, router, `{"image":"alpine:3.22","ttl_seconds":3601}`, "same-key")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	var conflictBody protocol.ErrorResponse
	if err := json.Unmarshal(conflict.Body.Bytes(), &conflictBody); err != nil || conflictBody.Error.Code != string(protocol.ErrorCodeIdempotencyConflict) {
		t.Fatalf("conflict body: %#v/%v", conflictBody, err)
	}

	withoutKeyA := performCreateRequest(t, router, `{"image":"alpine:3.22"}`, "")
	withoutKeyB := performCreateRequest(t, router, `{"image":"alpine:3.22"}`, "")
	if withoutKeyA.Code != http.StatusAccepted || withoutKeyB.Code != http.StatusAccepted ||
		withoutKeyA.Header().Get("Location") == withoutKeyB.Header().Get("Location") {
		t.Fatalf("non-idempotent creates: A=%d/%s B=%d/%s", withoutKeyA.Code, withoutKeyA.Header().Get("Location"), withoutKeyB.Code, withoutKeyB.Header().Get("Location"))
	}
	limited := performCreateRequest(t, router, `{"image":"alpine:3.22"}`, "")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("quota response: status=%d body=%s", limited.Code, limited.Body.String())
	}
	sandboxes, err := store.ListAll(context.Background())
	if err != nil || len(sandboxes) != 3 {
		t.Fatalf("stored sandboxes: len=%d err=%v", len(sandboxes), err)
	}
	if calls := waker.WakeCalls(); len(calls) != 3 {
		t.Fatalf("wake calls: %v", calls)
	}
}

func performCreateRequest(t *testing.T, handler http.Handler, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
