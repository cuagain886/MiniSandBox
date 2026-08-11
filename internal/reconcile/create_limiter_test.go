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

// TestCreateLimiterBoundsDifferentSandboxes 验证所有 ID 的 Ensure 共享同一上限。
func TestCreateLimiterBoundsDifferentSandboxes(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(2)
	runtime := &limitRecordingRuntime{started: make(chan struct{}, 5), release: make(chan struct{})}
	reconciler := &Reconciler{runtime: runtime, createLimiter: limiter}
	var wait sync.WaitGroup
	for index := 0; index < 5; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _ = reconciler.ensureRuntime(context.Background(), domain.Sandbox{ID: string(rune('a' + index))})
		}(index)
	}
	waitLimiterSignal(t, runtime.started)
	waitLimiterSignal(t, runtime.started)
	select {
	case <-runtime.started:
		t.Fatal("create limiter admitted a third Ensure")
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.release)
	wait.Wait()
	if got := runtime.maximum(); got != 2 {
		t.Fatalf("maximum Ensure concurrency: got %d, want 2", got)
	}
}

// TestCreateLimiterCancellationDoesNotCallRuntime 验证等待取消不触发 Runtime，
// 也不会被 reconciler 当成一次 runtime failure。
func TestCreateLimiterCancellationDoesNotCallRuntime(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	release, _ := limiter.Acquire(context.Background())
	defer release()
	runtime := &limitRecordingRuntime{}
	reconciler := &Reconciler{runtime: runtime, createLimiter: limiter}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := reconciler.ensureRuntime(ctx, domain.Sandbox{ID: "waiting"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Ensure: got %v, want deadline", err)
	}
	if runtime.callCount() != 0 {
		t.Fatalf("Runtime.Ensure was called %d times", runtime.callCount())
	}
}

// TestCreateLimiterWaitDoesNotPersistRetry 验证完整 reconcile 在取得 slot 前取消时
// 只保留可扫描的 Creating 状态，不清理 runtime 或增加 attempt。
func TestCreateLimiterWaitDoesNotPersistRetry(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	release, _ := limiter.Acquire(context.Background())
	defer release()
	events := make([]string, 0, 2)
	sandboxStore := newReconcileStore(&events, pendingSandbox())
	runtime := &limitRecordingRuntime{}
	reconciler := New(sandboxStore, runtime, &recordingProbe{events: &events})
	reconciler.createLimiter = limiter
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := reconciler.Reconcile(ctx, "sandbox-id"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconcile wait: got %v, want deadline", err)
	}
	if sandboxStore.record.RetryAttempt != 0 || len(sandboxStore.retryCalls) != 0 || runtime.callCount() != 0 {
		t.Fatalf("wait was accounted as failure: record=%#v calls=%d", sandboxStore.record, runtime.callCount())
	}
}

// TestCreateLimiterReleasesAfterRuntimePanic 验证 defer 在异常路径归还 slot。
func TestCreateLimiterReleasesAfterRuntimePanic(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	runtime := &limitRecordingRuntime{panicEnsure: true}
	reconciler := &Reconciler{runtime: runtime, createLimiter: limiter}
	func() {
		defer func() { _ = recover() }()
		_, _ = reconciler.ensureRuntime(context.Background(), domain.Sandbox{ID: "panic"})
	}()
	runtime.mu.Lock()
	runtime.panicEnsure = false
	runtime.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := reconciler.ensureRuntime(ctx, domain.Sandbox{ID: "after-panic"}); err != nil {
		t.Fatalf("slot leaked after panic: %v", err)
	}
}

// TestCreateLimiterDoesNotBlockDelete 验证 create 配额与删除路径相互独立。
func TestCreateLimiterDoesNotBlockDelete(t *testing.T) {
	limiter, _ := runtimeport.NewLimiter(1)
	release, _ := limiter.Acquire(context.Background())
	defer release()
	runtime := &limitRecordingRuntime{}
	if err := runtime.Delete(context.Background(), "deleting"); err != nil || runtime.deleteCount != 1 {
		t.Fatalf("delete was coupled to create limiter: calls=%d err=%v", runtime.deleteCount, err)
	}
}

type limitRecordingRuntime struct {
	mu          sync.Mutex
	started     chan struct{}
	release     chan struct{}
	active      int
	maxActive   int
	calls       int
	deleteCount int
	panicEnsure bool
}

func (r *limitRecordingRuntime) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	r.mu.Lock()
	r.calls++
	if r.panicEnsure {
		r.mu.Unlock()
		panic("ensure panic")
	}
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	started, release := r.started, r.release
	r.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return runtimeport.ActualSandbox{}, nil
}

func (*limitRecordingRuntime) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, nil
}

func (r *limitRecordingRuntime) Delete(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleteCount++
	return nil
}

func (*limitRecordingRuntime) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}

func (r *limitRecordingRuntime) maximum() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

func (r *limitRecordingRuntime) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func waitLimiterSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for limiter signal")
	}
}

var _ runtimeport.Runtime = (*limitRecordingRuntime)(nil)
