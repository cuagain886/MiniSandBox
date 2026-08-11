package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/reconcile"
)

// TestShutdownCoordinatorUsesSafeOrderAndIsIdempotent 验证撤销准入、停止生产者、
// 关闭队列、等待 worker、释放依赖的固定顺序只执行一次。
func TestShutdownCoordinatorUsesSafeOrderAndIsIdempotent(t *testing.T) {
	events := make([]string, 0, 5)
	readiness := readyShutdownState()
	queue := reconcile.NewWakeQueue()
	queue.Wake("pending")
	coordinator := &shutdownCoordinator{
		grace: time.Second, readiness: readiness,
		admission: shutdownContextStep{run: func(context.Context) error {
			if readiness.Snapshot().Ready() {
				t.Fatal("admission closed before readiness was revoked")
			}
			events = append(events, "admission")
			return nil
		}},
		maintenance: shutdownContextStep{run: func(context.Context) error {
			events = append(events, "maintenance")
			return nil
		}},
		queue: queue,
		worker: shutdownContextStep{run: func(context.Context) error {
			if id, err := queue.Next(context.Background()); id != "" || !errors.Is(err, reconcile.ErrWakeQueueClosed) {
				t.Fatalf("worker could start queued work during shutdown: id=%q err=%v", id, err)
			}
			events = append(events, "worker")
			return nil
		}},
		runtime: shutdownIOStep{run: func() error { events = append(events, "runtime"); return nil }},
		store:   shutdownIOStep{run: func() error { events = append(events, "store"); return nil }},
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
	if want := []string{"admission", "maintenance", "worker", "runtime", "store"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("shutdown order: got %v want %v", events, want)
	}
}

// TestShutdownCoordinatorUsesOneTotalGrace 验证前序阶段耗尽 grace 后仍返回安全
// deadline 诊断，并按顺序尝试关闭最终依赖。
func TestShutdownCoordinatorUsesOneTotalGrace(t *testing.T) {
	var mu sync.Mutex
	closed := make([]string, 0, 2)
	coordinator := &shutdownCoordinator{
		grace: 20 * time.Millisecond, readiness: readyShutdownState(),
		admission:   shutdownContextStep{run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		maintenance: shutdownContextStep{run: func(ctx context.Context) error { return ctx.Err() }},
		queue:       reconcile.NewWakeQueue(),
		worker:      shutdownContextStep{run: func(ctx context.Context) error { return ctx.Err() }},
		runtime:     shutdownIOStep{run: func() error { mu.Lock(); closed = append(closed, "runtime"); mu.Unlock(); return nil }},
		store:       shutdownIOStep{run: func() error { mu.Lock(); closed = append(closed, "store"); mu.Unlock(); return nil }},
	}
	started := time.Now()
	err := coordinator.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("shutdown exceeded total grace: %v", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(closed, []string{"runtime", "store"}) {
		t.Fatalf("final dependencies: %v", closed)
	}
}

type shutdownContextStep struct{ run func(context.Context) error }

func (s shutdownContextStep) Close(ctx context.Context) error { return s.run(ctx) }

type shutdownIOStep struct{ run func() error }

func (s shutdownIOStep) Close() error { return s.run() }

func readyShutdownState() *controlapi.Readiness {
	readiness := &controlapi.Readiness{}
	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	readiness.SetWorker(true)
	return readiness
}
