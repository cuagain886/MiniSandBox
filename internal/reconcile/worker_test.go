package reconcile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	runtimeport "minisandbox/internal/runtime"
)

// TestWorkerConsumesQueuedIDsSerially 验证单 worker 依次执行且自动 Done。
func TestWorkerConsumesQueuedIDsSerially(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	queue.Wake("sandbox-b")
	var mu sync.Mutex
	var processed []string
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	worker := mustWorker(t, queue, func(_ context.Context, id string) error {
		mu.Lock()
		processed = append(processed, id)
		count := len(processed)
		mu.Unlock()
		if count == 2 {
			close(finished)
		}
		return nil
	}, nil)

	done := runWorker(worker, ctx)
	waitSignal(t, finished)
	cancel()
	waitSignal(t, done)
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(processed, []string{"sandbox-a", "sandbox-b"}) {
		t.Fatalf("processed IDs: %v", processed)
	}
}

// TestWorkerPoolRunsDifferentIDsUpToConfiguredLimit 验证不同 ID 并发执行，且活跃
// reconcile 数从不超过固定 worker 数。
func TestWorkerPoolRunsDifferentIDsUpToConfiguredLimit(t *testing.T) {
	queue := NewWakeQueue()
	for index := 0; index < 6; index++ {
		queue.Wake(fmt.Sprintf("sandbox-%d", index))
	}
	started := make(chan struct{}, 6)
	finished := make(chan struct{}, 6)
	release := make(chan struct{})
	var mu sync.Mutex
	active, maximum := 0, 0
	worker, err := NewWorkerPool(queue, 2, time.Second, func(context.Context, string) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		finished <- struct{}{}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("new worker pool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	waitSignal(t, started)
	waitSignal(t, started)
	select {
	case <-started:
		t.Fatal("worker pool exceeded configured concurrency")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range 6 {
		waitSignal(t, finished)
	}
	cancel()
	waitSignal(t, done)
	mu.Lock()
	defer mu.Unlock()
	if maximum != 2 {
		t.Fatalf("maximum concurrency: got %d, want 2", maximum)
	}
}

// TestWorkerPoolSerializesReenteredID 验证处理中的同一 ID 只形成一次后续执行，
// 不会被空闲 worker 并发取走。
func TestWorkerPoolSerializesReenteredID(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-shared")
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	active, maximum, calls := 0, 0, 0
	worker, err := NewWorkerPool(queue, 4, time.Second, func(context.Context, string) error {
		mu.Lock()
		active++
		calls++
		call := calls
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		} else {
			close(secondStarted)
		}
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("new worker pool: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	waitSignal(t, firstStarted)
	if !queue.Wake("sandbox-shared") || queue.Wake("sandbox-shared") {
		t.Fatal("same-ID reentry was not merged")
	}
	select {
	case <-secondStarted:
		t.Fatal("same ID ran concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	waitSignal(t, secondStarted)
	cancel()
	waitSignal(t, done)
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || maximum != 1 {
		t.Fatalf("same-ID executions: calls=%d maximum=%d", calls, maximum)
	}
}

// TestWorkerReportsErrorAndContinues 验证单次错误不会阻断下一个任务。
func TestWorkerReportsErrorAndContinues(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	queue.Wake("sandbox-b")
	cause := errors.New("reconcile failed")
	reported := make(chan error, 1)
	second := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	worker := mustWorker(t, queue, func(_ context.Context, id string) error {
		if id == "sandbox-a" {
			return cause
		}
		close(second)
		return nil
	}, func(err error) {
		reported <- err
	})

	done := runWorker(worker, ctx)
	waitSignal(t, second)
	cancel()
	waitSignal(t, done)
	select {
	case err := <-reported:
		if !errors.Is(err, cause) {
			t.Fatalf("reported error: %v", err)
		}
	default:
		t.Fatal("reconcile error was not reported")
	}
}

// TestWorkerAppliesPerTaskTimeout 验证 reconcile 使用独立 deadline。
func TestWorkerAppliesPerTaskTimeout(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	reported := make(chan error, 1)
	worker, err := NewWorker(
		queue,
		20*time.Millisecond,
		func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
		func(err error) {
			reported <- err
		},
	)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)
	select {
	case err := <-reported:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not enforce reconcile timeout")
	}
	cancel()
	waitSignal(t, done)
}

// TestWorkerConvertsPanicAndStaysAlive 验证 panic 安全分类后继续消费。
func TestWorkerConvertsPanicAndStaysAlive(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	queue.Wake("sandbox-b")
	reported := make(chan error, 1)
	second := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	worker := mustWorker(t, queue, func(_ context.Context, id string) error {
		if id == "sandbox-a" {
			panic("secret panic value")
		}
		close(second)
		return nil
	}, func(err error) {
		reported <- err
	})

	done := runWorker(worker, ctx)
	waitSignal(t, second)
	cancel()
	waitSignal(t, done)
	select {
	case err := <-reported:
		failure := runtimeport.ClassifyError(err)
		if failure.Reason != runtimeport.FailureReasonInternalError ||
			err.Error() != "sandbox reconcile panicked" {
			t.Fatalf("panic classification: err=%v failure=%#v", err, failure)
		}
	default:
		t.Fatal("panic was not reported")
	}
}

// TestWorkerShutdownWaitsForCurrentTaskAndSkipsNewTask 验证优雅停止边界。
func TestWorkerShutdownWaitsForCurrentTaskAndSkipsNewTask(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var processed []string
	ctx, cancel := context.WithCancel(context.Background())
	worker := mustWorker(t, queue, func(ctx context.Context, id string) error {
		mu.Lock()
		processed = append(processed, id)
		mu.Unlock()
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	}, nil)

	done := runWorker(worker, ctx)
	waitSignal(t, started)
	queue.Wake("sandbox-b")
	cancel()
	select {
	case <-done:
		t.Fatal("worker returned before current task completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitSignal(t, done)

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(processed, []string{"sandbox-a"}) {
		t.Fatalf("worker started task after shutdown: %v", processed)
	}
	if queue.Len() != 1 {
		t.Fatalf("pending task was lost during shutdown: %d", queue.Len())
	}
}

// TestNewWorkerRejectsInvalidConfiguration 验证 worker 不接受不可运行配置。
func TestNewWorkerRejectsInvalidConfiguration(t *testing.T) {
	reconcile := func(context.Context, string) error { return nil }
	tests := []struct {
		name      string
		queue     *WakeQueue
		timeout   time.Duration
		reconcile ReconcileFunc
	}{
		{"nil queue", nil, time.Second, reconcile},
		{"invalid timeout", NewWakeQueue(), 0, reconcile},
		{"nil reconcile", NewWakeQueue(), time.Second, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewWorker(
				tt.queue,
				tt.timeout,
				tt.reconcile,
				nil,
			); err == nil {
				t.Fatal("invalid worker configuration was accepted")
			}
		})
	}
}

// TestNewWorkerPoolRejectsInvalidCount 验证并发度必须显式为正数。
func TestNewWorkerPoolRejectsInvalidCount(t *testing.T) {
	if _, err := NewWorkerPool(NewWakeQueue(), 0, time.Second, func(context.Context, string) error { return nil }, nil); err == nil {
		t.Fatal("zero worker count was accepted")
	}
}

// mustWorker 创建使用一秒 task timeout 的测试 worker。
func mustWorker(
	t *testing.T,
	queue *WakeQueue,
	reconcile ReconcileFunc,
	report ErrorReporter,
) *Worker {
	t.Helper()
	worker, err := NewWorker(queue, time.Second, reconcile, report)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	return worker
}

// runWorker 异步运行 worker 并返回退出通知。
func runWorker(worker *Worker, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	return done
}

// waitSignal 使用统一 deadline 等待测试事件。
func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for worker event")
	}
}
