package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// TestDeleteLimiterBoundsAllCleanupSources 验证 delete、expire、cleanup 和 recovery
// 语义来源最终共享同一个 Runtime.Delete 上限。
func TestDeleteLimiterBoundsAllCleanupSources(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(2)
	runtime := &limitRecordingRuntime{deleteStarted: make(chan struct{}, 4), deleteRelease: make(chan struct{})}
	reconciler := &Reconciler{runtime: runtime, deleteLimiter: limiter}
	sources := []string{"delete", "expire", "cleanup", "recovery"}
	var wait sync.WaitGroup
	for _, source := range sources {
		wait.Add(1)
		go func(source string) {
			defer wait.Done()
			_ = reconciler.deleteRuntime(context.Background(), source)
		}(source)
	}
	waitLimiterSignal(t, runtime.deleteStarted)
	waitLimiterSignal(t, runtime.deleteStarted)
	select {
	case <-runtime.deleteStarted:
		t.Fatal("delete limiter admitted a third cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.deleteRelease)
	wait.Wait()
	if got := runtime.deleteMaximum(); got != 2 {
		t.Fatalf("maximum delete concurrency: got %d, want 2", got)
	}
}

// TestDeleteLimiterWaitKeepsTerminationIntent 验证等待 slot 取消后不增加 retry，
// 已持久化的 DesiredTerminated/Stopping 仍可由 scanner 再次发现。
func TestDeleteLimiterWaitKeepsTerminationIntent(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	release, _ := limiter.Acquire(context.Background())
	defer release()
	events := make([]string, 0, 2)
	sandbox := pendingSandbox()
	sandbox.DesiredState = domain.DesiredTerminated
	sandboxStore := newReconcileStore(&events, sandbox)
	runtime := &limitRecordingRuntime{}
	reconciler := New(sandboxStore, runtime, &recordingProbe{events: &events})
	reconciler.deleteLimiter = limiter
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := reconciler.Reconcile(ctx, sandbox.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete wait: got %v, want deadline", err)
	}
	if sandboxStore.record.DesiredState != domain.DesiredTerminated || sandboxStore.record.ObservedState != domain.StateStopping || sandboxStore.record.RetryAttempt != 0 || len(sandboxStore.retryCalls) != 0 {
		t.Fatalf("delete intent was lost or accounted: %#v", sandboxStore.record)
	}
}

// TestDeleteLimiterReleasesAfterRuntimePanic 验证异常删除不会永久占用 slot。
func TestDeleteLimiterReleasesAfterRuntimePanic(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	runtime := &limitRecordingRuntime{panicDelete: true}
	reconciler := &Reconciler{runtime: runtime, deleteLimiter: limiter}
	func() {
		defer func() { _ = recover() }()
		_ = reconciler.deleteRuntime(context.Background(), "panic")
	}()
	runtime.mu.Lock()
	runtime.panicDelete = false
	runtime.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := reconciler.deleteRuntime(ctx, "after-panic"); err != nil {
		t.Fatalf("slot leaked after delete panic: %v", err)
	}
}

// TestCreateAndDeleteLimitersAreIndependent 验证满载 delete 不阻塞 create。
func TestCreateAndDeleteLimitersAreIndependent(t *testing.T) {
	createLimiter, _ := runtimeport.NewLimiter(1)
	deleteLimiter, _ := runtimeport.NewLimiter(1)
	releaseDelete, _ := deleteLimiter.Acquire(context.Background())
	defer releaseDelete()
	runtime := &limitRecordingRuntime{}
	reconciler := &Reconciler{runtime: runtime, createLimiter: createLimiter, deleteLimiter: deleteLimiter}
	if _, err := reconciler.ensureRuntime(context.Background(), domain.Sandbox{ID: "creating"}); err != nil {
		t.Fatalf("create was coupled to delete limiter: %v", err)
	}
}
