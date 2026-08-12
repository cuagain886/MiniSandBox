package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
	sqlitestore "minisandbox/internal/store/sqlite"
)

// cleanupFaultRuntime 模拟 Docker 删除编排中的三个独立资源，并允许每一阶段失败指定次数。
// 它只用于验证 reconciler 的持久化恢复，不替代 Docker adapter 自身的删除编排测试。
type cleanupFaultRuntime struct {
	mu        sync.Mutex
	remaining map[string]bool
	failures  map[string]int
	calls     int
}

func newCleanupFaultRuntime(failures map[string]int) *cleanupFaultRuntime {
	return &cleanupFaultRuntime{
		remaining: map[string]bool{"container": true, "volume": true, "runtime-dir": true},
		failures:  failures,
	}
}

func (*cleanupFaultRuntime) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected ensure")
}

func (*cleanupFaultRuntime) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected inspect")
}

func (r *cleanupFaultRuntime) Delete(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	var joined []error
	for _, resource := range []string{"container", "volume", "runtime-dir"} {
		if !r.remaining[resource] {
			continue
		}
		if r.failures[resource] > 0 {
			r.failures[resource]--
			joined = append(joined, fmt.Errorf("injected %s cleanup failure", resource))
			continue
		}
		r.remaining[resource] = false
	}
	return errors.Join(joined...)
}

func (*cleanupFaultRuntime) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}

func (r *cleanupFaultRuntime) snapshot() (map[string]bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	remaining := make(map[string]bool, len(r.remaining))
	for resource, exists := range r.remaining {
		remaining[resource] = exists
	}
	return remaining, r.calls
}

// TestCleanupPendingAutomaticallyRecoversFromPersistedSchedule 验证清理故障解除后，普通 candidate
// sweep 会依据 SQLite 中的绝对 next time 自动恢复，不需要用户再次发起删除请求。
func TestCleanupPendingAutomaticallyRecoversFromPersistedSchedule(t *testing.T) {
	tests := []struct {
		name     string
		failures map[string]int
		attempts int
	}{
		{name: "container", failures: map[string]int{"container": 1}, attempts: 1},
		{name: "volume", failures: map[string]int{"volume": 1}, attempts: 1},
		{name: "runtime directory", failures: map[string]int{"runtime-dir": 1}, attempts: 1},
		{name: "aggregated", failures: map[string]int{"container": 1, "volume": 1, "runtime-dir": 1}, attempts: 1},
		{name: "multiple retries", failures: map[string]int{"container": 2, "volume": 1}, attempts: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sandboxd.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := database.Migrate(ctx); err != nil {
				t.Fatal(err)
			}

			now := time.Date(2027, 9, 10, 11, 12, 13, 0, time.UTC)
			record := cleanupRecoveryRecord(now)
			if err := database.Create(ctx, record); err != nil {
				t.Fatal(err)
			}
			runtime := newCleanupFaultRuntime(test.failures)
			clock := &restartClock{now: now}
			reconciler, err := NewWithRetry(database, runtime, restartProbe{}, clock, restartRandom{}, time.Second, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			sweeper, err := NewCandidateSweeper(database, 10, 10, time.Second, time.Minute)
			if err != nil {
				t.Fatal(err)
			}

			if err := reconciler.Reconcile(ctx, record.ID); err == nil {
				t.Fatal("injected cleanup failure unexpectedly converged")
			}
			for expectedAttempt := 1; expectedAttempt <= test.attempts; expectedAttempt++ {
				failed, err := database.Get(ctx, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				if failed.ObservedState != domain.StateFailed || failed.Reason != domain.SandboxReasonCleanupPending ||
					failed.RetryAttempt != uint32(expectedAttempt) || failed.NextReconcileAt == nil {
					t.Fatalf("cleanup retry metadata at attempt %d: %#v", expectedAttempt, failed)
				}
				clock.now = failed.NextReconcileAt.UTC()
				if err := sweepAndReconcile(ctx, sweeper, reconciler, clock.now); expectedAttempt < test.attempts && err == nil {
					t.Fatal("remaining injected fault unexpectedly converged")
				} else if expectedAttempt == test.attempts && err != nil {
					t.Fatal(err)
				}
			}

			converged, err := database.Get(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if converged.ObservedState != domain.StateTerminated || converged.Reason != domain.SandboxReasonTerminated ||
				converged.RetryAttempt != 0 || converged.NextReconcileAt != nil || converged.RuntimeID != "" {
				t.Fatalf("cleanup did not reach the frozen terminal contract: %#v", converged)
			}
			remaining, calls := runtime.snapshot()
			if remaining["container"] || remaining["volume"] || remaining["runtime-dir"] {
				t.Fatalf("managed resources remain after recovery: %v", remaining)
			}
			if calls != test.attempts+1 {
				t.Fatalf("delete calls=%d want %d", calls, test.attempts+1)
			}
		})
	}
}

func cleanupRecoveryRecord(now time.Time) domain.Sandbox {
	expires := now.Add(time.Hour)
	return domain.Sandbox{
		ID: "10010203-0405-4607-8809-0a0b0c0d0e0f",
		Spec: domain.SandboxSpec{
			Image:     "example.invalid/image:fixed",
			Resources: domain.ResourceLimits{CPUQuotaMillis: 100, MemoryMiB: 64, PIDs: 16},
			Workspace: domain.WorkspaceSpec{MountPath: "/workspace"},
			Platform:  domain.Platform{OS: "linux", Arch: "amd64"},
		},
		DesiredState: domain.DesiredTerminated, ObservedState: domain.StateRunning,
		Reason: domain.SandboxReasonRunning, Message: "running", RuntimeID: "runtime-1",
		SpecHash: "hash", Revision: 1, CreatedAt: now, UpdatedAt: now, LastTransitionAt: now,
		ExpiresAt: &expires, Origin: domain.SandboxOriginAPI,
	}
}

func sweepAndReconcile(ctx context.Context, sweeper *CandidateSweeper, reconciler *Reconciler, now time.Time) error {
	return sweeper.Sweep(ctx, now, func(ctx context.Context, page []domain.Sandbox) error {
		for _, candidate := range page {
			if err := reconciler.Reconcile(ctx, candidate.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

var _ runtimeport.Runtime = (*cleanupFaultRuntime)(nil)
var _ storeport.Store = (*sqlitestore.Store)(nil)
