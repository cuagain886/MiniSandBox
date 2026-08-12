package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type operationPeakRegistry struct {
	mu      sync.Mutex
	active  map[string]int
	maximum map[string]int
}

func newOperationPeakRegistry() *operationPeakRegistry {
	return &operationPeakRegistry{active: make(map[string]int), maximum: make(map[string]int)}
}

func (r *operationPeakRegistry) enter(operation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[operation]++
	if r.active[operation] > r.maximum[operation] {
		r.maximum[operation] = r.active[operation]
	}
}

func (r *operationPeakRegistry) leave(operation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[operation]--
}

func (r *operationPeakRegistry) snapshot(operation string) (active, maximum int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[operation], r.maximum[operation]
}

// TestLifecycleOperationLimitersRespectIndependentConfiguredPeaks 验证 create、image pull、
// delete 三类生产门禁在同一压力窗口中各自遵守 1/N 上限，互不借用或泄漏 slot。
func TestLifecycleOperationLimitersRespectIndependentConfiguredPeaks(t *testing.T) {
	limits := map[string]int{"create": 1, "image-pull": 2, "delete": 3}
	registry := newOperationPeakRegistry()
	for operation, limit := range limits {
		operation, limit := operation, limit
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			limiter, err := NewLimiter(limit)
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, limit+4)
			releaseWork := make(chan struct{})
			var wait sync.WaitGroup
			for range limit + 4 {
				wait.Add(1)
				go func() {
					defer wait.Done()
					release, err := limiter.Acquire(context.Background())
					if err != nil {
						return
					}
					defer release()
					registry.enter(operation)
					defer registry.leave(operation)
					started <- struct{}{}
					<-releaseWork
				}()
			}
			for range limit {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("configured slots were not admitted")
				}
			}
			select {
			case <-started:
				t.Fatal("operation exceeded configured concurrency")
			case <-time.After(20 * time.Millisecond):
			}
			close(releaseWork)
			wait.Wait()
			active, maximum := registry.snapshot(operation)
			if active != 0 || maximum != limit {
				t.Fatalf("registry: active=%d maximum=%d limit=%d", active, maximum, limit)
			}
		})
	}
}

// TestOperationLimiterFailureCancelAndRestartDoNotLeakSlots 验证失败 defer、等待取消和新进程
// 代际重建均不会留下幽灵占用；旧代 limiter 的内存状态不是可恢复事实。
func TestOperationLimiterFailureCancelAndRestartDoNotLeakSlots(t *testing.T) {
	oldGeneration, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	releaseFailed, err := oldGeneration.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseFailed()

	releaseHeld, err := oldGeneration.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := oldGeneration.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled wait: %v", err)
	}

	// 重启后门禁按配置重新构造；运行中操作由 Store/reconcile 恢复，绝不恢复进程内 slot。
	newGeneration, err := NewLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	newRelease, err := newGeneration.Acquire(context.Background())
	if err != nil {
		t.Fatalf("new generation inherited stale slot: %v", err)
	}
	newRelease()
	releaseHeld()
	if finalRelease, err := oldGeneration.Acquire(context.Background()); err != nil {
		t.Fatalf("old generation slot leaked after cancellation: %v", err)
	} else {
		finalRelease()
	}
}
