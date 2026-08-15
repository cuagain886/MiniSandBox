package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// fakeLifecycleServer 按脚本驱动 sandbox 生命周期状态机，用于验收高层
// 易用接口的完整闭环，不依赖真实 Docker 环境。
type fakeLifecycleServer struct {
	mu      sync.Mutex
	state   protocol.SandboxState
	reason  protocol.SandboxReason
	gets    int
	deleted bool
}

func (f *fakeLifecycleServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			var request protocol.CreateSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			f.state = protocol.SandboxStatePending
			f.reason = protocol.SandboxReasonCreateAccepted
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(f.sandbox("sbx-lifecycle"))
		case r.Method == http.MethodGet:
			f.gets++
			if f.deleted {
				// 删除后的第一次查询保持 Stopping，之后收敛到 Terminated。
				if f.gets == 1 {
					f.state = protocol.SandboxStateStopping
					f.reason = protocol.SandboxReasonDeletingRuntime
				} else {
					f.state = protocol.SandboxStateTerminated
					f.reason = protocol.SandboxReasonTerminated
				}
			} else {
				// 创建后前两次查询保持 Pending/Creating，第三次收敛到 Running。
				switch f.gets {
				case 1:
					f.state = protocol.SandboxStatePending
				case 2:
					f.state = protocol.SandboxStateCreating
					f.reason = protocol.SandboxReasonCreatingRuntime
				default:
					f.state = protocol.SandboxStateRunning
					f.reason = protocol.SandboxReasonRunning
				}
			}
			_ = json.NewEncoder(w).Encode(f.sandbox("sbx-lifecycle"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-lifecycle/renew":
			var request protocol.RenewSandboxRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode renew request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			f.state = protocol.SandboxStateRunning
			f.reason = protocol.SandboxReasonRunning
			_ = json.NewEncoder(w).Encode(protocol.Sandbox{
				ID:        "sbx-lifecycle",
				State:     f.state,
				Reason:    f.reason,
				Image:     "debian:bookworm-slim",
				ExpiresAt: request.ExpiresAt,
				CreatedAt: time.Unix(1000, 0).UTC(),
				UpdatedAt: time.Unix(1001, 0).UTC(),
			})
		case r.Method == http.MethodDelete:
			// 删除后查询先保持 Stopping，再进入 Terminated。
			f.deleted = true
			f.gets = 0
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

func (f *fakeLifecycleServer) sandbox(id string) protocol.Sandbox {
	return protocol.Sandbox{
		ID:        id,
		State:     f.state,
		Reason:    f.reason,
		Image:     "debian:bookworm-slim",
		ExpiresAt: time.Unix(2000, 0).UTC(),
		CreatedAt: time.Unix(1000, 0).UTC(),
		UpdatedAt: time.Unix(1001, 0).UTC(),
	}
}

// TestSandboxLifecycleFacade 验收 create → WaitRunning → Info → Renew →
// DeleteAndWait 完整闭环，并确认底层 API 仍可独立完成同样请求。
func TestSandboxLifecycleFacade(t *testing.T) {
	fake := &fakeLifecycleServer{}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.Create(ctx, CreateSandboxRequest{
		Image: "debian:bookworm-slim",
	}, WithIdempotencyKey("lifecycle-001"))
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID() != "sbx-lifecycle" {
		t.Fatalf("unexpected sandbox ID: %s", created.ID())
	}

	running, err := created.WaitRunning(ctx)
	if err != nil {
		t.Fatalf("wait running: %v", err)
	}
	if running.State != SandboxStateRunning || running.ID != "sbx-lifecycle" {
		t.Fatalf("unexpected running info: %#v", running)
	}

	info, err := created.Info(ctx)
	if err != nil || info.State != SandboxStateRunning {
		t.Fatalf("info: %v (%#v)", err, info)
	}

	newExpiry := info.ExpiresAt.Add(10 * time.Minute)
	renewed, err := created.Renew(ctx, newExpiry)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renew expiry mismatch: got %s want %s", renewed.ExpiresAt, newExpiry)
	}

	terminated, err := created.DeleteAndWait(ctx)
	if err != nil {
		t.Fatalf("delete and wait: %v", err)
	}
	if terminated.State != SandboxStateTerminated {
		t.Fatalf("unexpected final state: %s", terminated.State)
	}

	// 底层 API 回归：同一 client 仍可直接完成 GetSandbox 查询。
	fake.mu.Lock()
	fake.state = protocol.SandboxStateTerminated
	fake.reason = protocol.SandboxReasonTerminated
	fake.gets = 10
	fake.mu.Unlock()
	raw, err := client.GetSandbox(ctx, "sbx-lifecycle")
	if err != nil || raw.State != protocol.SandboxStateTerminated {
		t.Fatalf("low-level GetSandbox regression: %v (%#v)", err, raw)
	}
}

// TestWaitRunningFailsOnTerminalState 验证未进入 Running 就到达 Failed 或
// Terminated 时 WaitRunning 立即失败并携带稳定 reason。
func TestWaitRunningFailsOnTerminalState(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		state protocol.SandboxState
	}{
		{"failed", protocol.SandboxStateFailed},
		{"terminated", protocol.SandboxStateTerminated},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter,
				_ *http.Request,
			) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(protocol.Sandbox{
					ID:      "sbx-doomed",
					State:   scenario.state,
					Reason:  protocol.SandboxReasonImagePullFailed,
					Message: "image pull failed",
				})
			}))
			defer server.Close()

			client := NewClient(server.URL, server.Client())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := client.Sandbox("sbx-doomed").WaitRunning(ctx)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, context.DeadlineExceeded) &&
				!containsAll(err.Error(), "sbx-doomed", "IMAGE_PULL_FAILED") {
				t.Fatalf("error should identify sandbox and reason: %v", err)
			}
		})
	}
}

// TestDeleteAndWaitFailsOnCleanupFailure 验证删除收敛到 Failed 时返回错误，
// 调用方可以安全重试。
func TestDeleteAndWaitFailsOnCleanupFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			requests++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Sandbox{
			ID:      "sbx-stuck",
			State:   protocol.SandboxStateFailed,
			Reason:  protocol.SandboxReasonCleanupPending,
			Message: "cleanup pending",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.Sandbox("sbx-stuck").DeleteAndWait(ctx)
	if err == nil || !containsAll(err.Error(), "sbx-stuck", "CLEANUP_PENDING") {
		t.Fatalf("expected cleanup failure error, got %v", err)
	}
}

// containsAll 简化多条子串断言。
func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
