package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestWakeQueueMergesDuplicateID 验证消费前的重复唤醒只占一个条目。
func TestWakeQueueMergesDuplicateID(t *testing.T) {
	queue := NewWakeQueue()
	if !queue.Wake("sandbox-a") {
		t.Fatal("first wake was not accepted")
	}
	for range 100 {
		if queue.Wake("sandbox-a") {
			t.Fatal("duplicate wake was accepted")
		}
	}
	if got := queue.Len(); got != 1 {
		t.Fatalf("length: got %d, want 1", got)
	}
	if got := nextQueueID(t, queue); got != "sandbox-a" {
		t.Fatalf("ID: got %q, want sandbox-a", got)
	}
}

// TestWakeQueuePreservesDifferentIDsWhenNotificationIsFull 验证单槽通知不会丢任务。
func TestWakeQueuePreservesDifferentIDsWhenNotificationIsFull(t *testing.T) {
	queue := NewWakeQueue()
	for _, id := range []string{"sandbox-a", "sandbox-b", "sandbox-c"} {
		if !queue.Wake(id) {
			t.Fatalf("wake %s was rejected", id)
		}
	}

	for index, want := range []string{"sandbox-a", "sandbox-b", "sandbox-c"} {
		if got := nextQueueID(t, queue); got != want {
			t.Fatalf("ID %d: got %q, want %q", index, got, want)
		}
		queue.Done(want)
	}
	if got := queue.Len(); got != 0 {
		t.Fatalf("length after drain: %d", got)
	}
}

// TestWakeQueueAllowsOneReentryWhileProcessing 验证取出后重复唤醒合并为一次后续任务。
func TestWakeQueueAllowsOneReentryWhileProcessing(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-a")
	if got := nextQueueID(t, queue); got != "sandbox-a" {
		t.Fatalf("first ID: %q", got)
	}
	if !queue.Wake("sandbox-a") {
		t.Fatal("processing ID could not be scheduled again")
	}
	if queue.Wake("sandbox-a") {
		t.Fatal("processing reentry was not merged")
	}
	queue.Done("sandbox-a")
	if got := nextQueueID(t, queue); got != "sandbox-a" {
		t.Fatalf("reentered ID: %q", got)
	}
}

// TestWakeQueueNextHonorsContextCancellation 验证 shutdown 不会凭空返回任务。
func TestWakeQueueNextHonorsContextCancellation(t *testing.T) {
	queue := NewWakeQueue()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	id, err := queue.Next(ctx)
	if id != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("Next: id=%q err=%v", id, err)
	}
}

// TestWakeQueueConcurrentWakeMergesByID 验证并发高频唤醒只保留唯一 ID。
func TestWakeQueueConcurrentWakeMergesByID(t *testing.T) {
	queue := NewWakeQueue()
	const uniqueIDs = 16
	var workers sync.WaitGroup
	for worker := range 32 {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := range 200 {
				queue.Wake(fmt.Sprintf(
					"sandbox-%02d",
					(worker+iteration)%uniqueIDs,
				))
			}
		}(worker)
	}
	workers.Wait()

	if got := queue.Len(); got != uniqueIDs {
		t.Fatalf("unique pending IDs: got %d, want %d", got, uniqueIDs)
	}
	seen := make(map[string]struct{}, uniqueIDs)
	for range uniqueIDs {
		id := nextQueueID(t, queue)
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate drained ID: %s", id)
		}
		seen[id] = struct{}{}
		queue.Done(id)
	}
}

// TestWakeQueueRejectsEmptyID 验证无效 ID 不占用通知或内存。
func TestWakeQueueRejectsEmptyID(t *testing.T) {
	queue := NewWakeQueue()
	if queue.Wake("") || queue.Len() != 0 {
		t.Fatal("empty ID entered queue")
	}
}

// TestWakeQueueCloseRejectsIntakeAndPendingDelivery 验证 shutdown 后既不接收新
// Wake，也不让空闲 worker 启动旧 pending；重复关闭保持幂等。
func TestWakeQueueCloseRejectsIntakeAndPendingDelivery(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-pending")
	queue.Close()
	queue.Close()
	if queue.Wake("sandbox-new") {
		t.Fatal("closed queue accepted wake")
	}
	if id, err := queue.Next(context.Background()); id != "" || !errors.Is(err, ErrWakeQueueClosed) {
		t.Fatalf("closed Next: id=%q err=%v", id, err)
	}
}

// TestWakeQueueMergesFourConcurrentSources 验证 API/recovery/TTL/scanner 共用同一状态项。
func TestWakeQueueMergesFourConcurrentSources(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-shared")
	if got := nextQueueID(t, queue); got != "sandbox-shared" {
		t.Fatalf("processing ID: %s", got)
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	accepted := make(chan bool, 4)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			accepted <- queue.Wake("sandbox-shared")
		}()
	}
	close(start)
	wait.Wait()
	close(accepted)
	acceptedCount := 0
	for value := range accepted {
		if value {
			acceptedCount++
		}
	}
	if acceptedCount != 1 || queue.Len() != 1 || len(queue.states) != 1 {
		t.Fatalf("merged sources: accepted=%d len=%d states=%d", acceptedCount, queue.Len(), len(queue.states))
	}
	queue.Done("sandbox-shared")
	if got := nextQueueID(t, queue); got != "sandbox-shared" {
		t.Fatalf("requeued ID: %s", got)
	}
	queue.Done("sandbox-shared")
	if len(queue.states) != 0 {
		t.Fatalf("idle state entries: %d", len(queue.states))
	}
}

// TestWakeQueueDoneRacePreservesLastIntent 验证 Wake 与 Done 任意先后都留下恰好一次后续处理。
func TestWakeQueueDoneRacePreservesLastIntent(t *testing.T) {
	for iteration := 0; iteration < 500; iteration++ {
		queue := NewWakeQueue()
		queue.Wake("sandbox-race")
		nextQueueID(t, queue)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); <-start; queue.Wake("sandbox-race") }()
		go func() { defer wait.Done(); <-start; queue.Done("sandbox-race") }()
		close(start)
		wait.Wait()
		if queue.Len() != 1 {
			t.Fatalf("iteration %d lost or duplicated intent: len=%d", iteration, queue.Len())
		}
		nextQueueID(t, queue)
		queue.Done("sandbox-race")
	}
}

// TestWakeQueueProcessingWakeMemoryBounded 验证高频 requeue 不按次数扩大 map 或 order。
func TestWakeQueueProcessingWakeMemoryBounded(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-hot")
	nextQueueID(t, queue)
	for range 100_000 {
		queue.Wake("sandbox-hot")
	}
	if queue.Len() != 1 || len(queue.states) != 1 || len(queue.order) != 0 {
		t.Fatalf("unbounded processing wake: len=%d states=%d order=%d", queue.Len(), len(queue.states), len(queue.order))
	}
	queue.Done("sandbox-hot")
	nextQueueID(t, queue)
	queue.Done("sandbox-hot")
}

// TestWakeQueueCancelledNextKeepsPending 验证 shutdown cancel 不消费尚未处理的持久化意图。
func TestWakeQueueCancelledNextKeepsPending(t *testing.T) {
	queue := NewWakeQueue()
	queue.Wake("sandbox-shutdown")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := queue.Next(ctx); id != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled next: id=%q err=%v", id, err)
	}
	if queue.Len() != 1 || nextQueueID(t, queue) != "sandbox-shutdown" {
		t.Fatal("cancelled Next lost pending ID")
	}
	queue.Done("sandbox-shutdown")
}

// nextQueueID 使用短 deadline 读取测试任务，避免通知丢失时测试永久阻塞。
func nextQueueID(t *testing.T, queue *WakeQueue) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id, err := queue.Next(ctx)
	if err != nil {
		t.Fatalf("next queue ID: %v", err)
	}
	if id == "" {
		t.Fatal("queue returned empty ID")
	}
	return id
}
