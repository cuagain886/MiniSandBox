package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	sqlitestore "minisandbox/internal/store/sqlite"
)

type restartFaultRuntime struct{ unavailable bool }

func (r *restartFaultRuntime) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	if r.unavailable {
		return runtimeport.ActualSandbox{}, &restartUnavailableError{}
	}
	return runtimeport.ActualSandbox{RuntimeID: "runtime-1", RunnerProtocolVersion: 1}, nil
}
func (*restartFaultRuntime) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, errors.New("unexpected inspect")
}
func (*restartFaultRuntime) Delete(context.Context, string) error { return nil }
func (*restartFaultRuntime) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}

type restartUnavailableError struct{}

func (*restartUnavailableError) Error() string     { return "runtime unavailable" }
func (*restartUnavailableError) Unavailable() bool { return true }
func (*restartUnavailableError) FailureReason() string {
	return runtimeport.FailureReasonRuntimeUnavailable
}

type restartProbe struct{}

func (restartProbe) Probe(context.Context, string, int) error { return nil }

type restartClock struct{ now time.Time }

func (c *restartClock) Now() time.Time               { return c.now }
func (*restartClock) NewTimer(time.Duration) Timer   { panic("unexpected timer") }
func (*restartClock) NewTicker(time.Duration) Ticker { panic("unexpected ticker") }

type restartRandom struct{}

func (restartRandom) Int64N(upper int64) (int64, error) { return upper - 1, nil }

// TestRetryScheduleSurvivesSQLiteRestartAndRecoversWithoutManualWake 验证 capped next 是持久事实而非进程内计时器。
func TestRetryScheduleSurvivesSQLiteRestartAndRecoversWithoutManualWake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandboxd.db")
	first, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	firstOpen := true
	defer func() {
		if firstOpen {
			_ = first.Close()
		}
	}()
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 7, 8, 9, 10, 11, 0, time.UTC)
	expires := now.Add(time.Hour)
	record := domain.Sandbox{ID: "00010203-0405-4607-8809-0a0b0c0d0e0f", Spec: domain.SandboxSpec{Image: "example.invalid/image:fixed", Resources: domain.ResourceLimits{CPUQuotaMillis: 100, MemoryMiB: 64, PIDs: 16}, Workspace: domain.WorkspaceSpec{MountPath: "/workspace"}, Platform: domain.Platform{OS: "linux", Arch: "amd64"}}, DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending, Reason: domain.SandboxReasonCreateAccepted, Message: "accepted", SpecHash: "hash", Revision: 1, CreatedAt: now, UpdatedAt: now, LastTransitionAt: now, ExpiresAt: &expires, Origin: domain.SandboxOriginAPI}
	if err := first.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	runtime := &restartFaultRuntime{unavailable: true}
	clock := &restartClock{now: now}
	reconciler, _ := NewWithRetry(first, runtime, restartProbe{}, clock, restartRandom{}, 10*time.Second, 10*time.Second)
	if err := reconciler.Reconcile(context.Background(), record.ID); err == nil {
		t.Fatal("outage unexpectedly converged")
	}
	failed, _ := first.Get(context.Background(), record.ID)
	if failed.RetryAttempt != 1 || failed.NextReconcileAt == nil || !failed.NextReconcileAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("persisted retry: %#v", failed)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstOpen = false
	second, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	reopened, _ := second.Get(context.Background(), record.ID)
	if reopened.NextReconcileAt == nil || !reopened.NextReconcileAt.Equal(*failed.NextReconcileAt) {
		t.Fatalf("restart changed next: before=%v after=%v", failed.NextReconcileAt, reopened.NextReconcileAt)
	}
	sweeper, _ := NewCandidateSweeper(second, 10, 10, time.Second, time.Minute)
	before := 0
	_ = sweeper.Sweep(context.Background(), now.Add(9*time.Second), func(context.Context, []domain.Sandbox) error { before++; return nil })
	if before != 0 {
		t.Fatal("retry ran before persisted next")
	}
	runtime.unavailable = false
	clock.now = now.Add(10 * time.Second)
	ids := []string{}
	_ = sweeper.Sweep(context.Background(), clock.now, func(_ context.Context, page []domain.Sandbox) error {
		for _, item := range page {
			ids = append(ids, item.ID)
		}
		return nil
	})
	if len(ids) != 1 || ids[0] != record.ID {
		t.Fatalf("due candidates: %v", ids)
	}
	reconciler, _ = NewWithRetry(second, runtime, restartProbe{}, clock, restartRandom{}, 10*time.Second, 10*time.Second)
	if err := reconciler.Reconcile(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	converged, _ := second.Get(context.Background(), record.ID)
	if converged.ObservedState != domain.StateRunning || converged.RetryAttempt != 0 || converged.NextReconcileAt != nil {
		t.Fatalf("recovered record: %#v", converged)
	}
}
