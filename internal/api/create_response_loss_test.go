package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/internal/testutil"
)

var errInjectedResponseWrite = errors.New("injected response write failure")

// failingResponseWriter 模拟响应头已提交后，body 在首字节或部分写入后丢失。
type failingResponseWriter struct {
	header    http.Header
	status    int
	body      []byte
	failAfter int
	err       error
}

// Header 返回本次失败响应仍可观察的 header。
func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

// WriteHeader 记录已经提交的 HTTP 状态。
func (w *failingResponseWriter) WriteHeader(status int) {
	w.status = status
}

// Write 在配置的字节边界返回 transport 错误。
func (w *failingResponseWriter) Write(body []byte) (int, error) {
	written := w.failAfter
	if written > len(body) {
		written = len(body)
	}
	w.body = append(w.body, body[:written]...)
	return written, w.err
}

// TestCreateResponseLossReplaysCommittedOutcome 验证提交后的各种响应丢失不会重复创建或 Wake。
func TestCreateResponseLossReplaysCommittedOutcome(t *testing.T) {
	tests := []struct {
		name       string
		failAfter  int
		writeError error
		cancel     bool
		restart    bool
	}{
		{name: "header_before_body", failAfter: 0, writeError: errInjectedResponseWrite},
		{name: "partial_body", failAfter: 31, writeError: errInjectedResponseWrite},
		{name: "client_cancel", failAfter: 0, writeError: context.Canceled, cancel: true},
		{name: "sandboxd_restart", failAfter: 0, writeError: errInjectedResponseWrite, restart: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sandboxd.db")
			currentStore := openResponseLossStore(t, path, true)
			t.Cleanup(func() { _ = currentStore.Close() })
			waker := testutil.NewFakeWaker()
			service := application.NewSandboxService(
				currentStore, nil, nil, application.SandboxSpecBuilder{}, waker,
			)

			attempt := 0
			var cancelBeforeWrite context.CancelFunc
			var writeErrors []error
			handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt++
				request := responseLossCreateRequest(fmt.Sprintf("response-loss-%02d", attempt))
				outcome, err := service.CommitIdempotentCreate(r.Context(), request)
				if err != nil {
					writeErrors = append(writeErrors, err)
					return
				}
				if cancelBeforeWrite != nil {
					cancelBeforeWrite()
					cancelBeforeWrite = nil
				}
				writeErrors = append(writeErrors, writeCreateOutcome(w, outcome))
			}))

			firstRequest := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", nil)
			if test.cancel {
				var cancel context.CancelFunc
				firstRequest = firstRequest.WithContext(context.Background())
				firstRequestContext, cancelRequest := context.WithCancel(firstRequest.Context())
				cancel = cancelRequest
				firstRequest = firstRequest.WithContext(firstRequestContext)
				cancelBeforeWrite = cancel
			}
			failed := &failingResponseWriter{
				header: make(http.Header), failAfter: test.failAfter, err: test.writeError,
			}
			handler.ServeHTTP(failed, firstRequest)
			if len(writeErrors) != 1 || !errors.Is(writeErrors[0], test.writeError) {
				t.Fatalf("first write error: %v", writeErrors)
			}
			if failed.status != http.StatusAccepted || failed.Header().Get("Location") != "/v1/sandboxes/response-loss-01" {
				t.Fatalf("first response metadata: status=%d header=%v", failed.status, failed.header)
			}
			firstRequestID := failed.Header().Get(requestIDHeader)
			if firstRequestID == "" {
				t.Fatal("first request id is empty")
			}
			assertResponseLossState(t, currentStore, waker, "response-loss-01")

			if test.restart {
				if err := currentStore.Close(); err != nil {
					t.Fatalf("close before restart: %v", err)
				}
				currentStore = openResponseLossStore(t, path, false)
				service = application.NewSandboxService(
					currentStore, nil, nil, application.SandboxSpecBuilder{}, waker,
				)
			}

			replayed := httptest.NewRecorder()
			handler.ServeHTTP(replayed, httptest.NewRequest(http.MethodPost, "/v1/sandboxes", nil))
			if len(writeErrors) != 2 || writeErrors[1] != nil {
				t.Fatalf("replay write errors: %v", writeErrors)
			}
			wantBody := responseLossCreateRequest("response-loss-01").Response.Body
			if replayed.Code != http.StatusAccepted || replayed.Header().Get("Location") != "/v1/sandboxes/response-loss-01" ||
				!errors.Is(writeErrors[0], test.writeError) || string(replayed.Body.Bytes()) != string(wantBody) {
				t.Fatalf("replayed response: status=%d location=%q body=%s", replayed.Code, replayed.Header().Get("Location"), replayed.Body.String())
			}
			secondRequestID := replayed.Header().Get(requestIDHeader)
			if secondRequestID == "" || secondRequestID == firstRequestID {
				t.Fatalf("request ids must be per attempt: first=%q second=%q", firstRequestID, secondRequestID)
			}
			assertResponseLossState(t, currentStore, waker, "response-loss-01")
		})
	}
}

// openResponseLossStore 打开测试数据库，并按需执行首次 migration。
func openResponseLossStore(t *testing.T, path string, migrate bool) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatalf("open response-loss store: %v", err)
	}
	if migrate {
		if err := store.Migrate(context.Background()); err != nil {
			_ = store.Close()
			t.Fatalf("migrate response-loss store: %v", err)
		}
	}
	return store
}

// responseLossCreateRequest 为每次 transport 尝试生成不同候选 ID，但保持同一幂等身份。
func responseLossCreateRequest(id string) storeport.IdempotentCreateRequest {
	createdAt := time.Date(2026, 8, 11, 9, 10, 11, 123456789, time.UTC)
	expiresAt := createdAt.Add(30 * time.Minute)
	spec := domain.SandboxSpec{
		Image: "busybox:1.36",
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath},
		Platform:  domain.Platform{OS: "linux", Arch: "amd64"},
	}
	body := []byte(fmt.Sprintf(
		`{"id":%q,"state":"Pending","reason":"CREATE_ACCEPTED","message":"Sandbox creation has been accepted.","image":"busybox:1.36","expires_at":%q,"created_at":%q,"updated_at":%q}`,
		id, expiresAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano),
	))
	return storeport.IdempotentCreateRequest{
		ScopeID:     "local:v1",
		Key:         "response-loss-key",
		RequestHash: strings.Repeat("a", 64),
		Sandbox: domain.Sandbox{
			ID: id, Spec: spec, DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending,
			Reason: domain.SandboxReasonCreateAccepted, Message: "Sandbox creation has been accepted.",
			SpecHash: spec.Hash(), CreatedAt: createdAt, UpdatedAt: createdAt, LastTransitionAt: createdAt,
			ExpiresAt: &expiresAt, Origin: domain.SandboxOriginAPI,
		},
		Response: storeport.IdempotentResponse{
			SchemaVersion: 1, StatusCode: http.StatusAccepted, Location: "/v1/sandboxes/" + id,
			Body: body, CreatedAt: createdAt,
		},
		MaxSandboxes: 10,
	}
}

// assertResponseLossState 验证失败和重放后都只有首个 sandbox，且首次提交只 Wake 一次。
func assertResponseLossState(t *testing.T, store *sqlitestore.Store, waker *testutil.FakeWaker, wantID string) {
	t.Helper()
	sandboxes, err := store.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list response-loss sandboxes: %v", err)
	}
	if len(sandboxes) != 1 || sandboxes[0].ID != wantID {
		t.Fatalf("response-loss sandboxes: %#v", sandboxes)
	}
	if calls := waker.WakeCalls(); len(calls) != 1 || calls[0] != wantID {
		t.Fatalf("response-loss wake calls: %v", calls)
	}
}
