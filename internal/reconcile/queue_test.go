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
		queue.Done()
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
	queue.Done()
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
		queue.Done()
	}
}

// TestWakeQueueRejectsEmptyID 验证无效 ID 不占用通知或内存。
func TestWakeQueueRejectsEmptyID(t *testing.T) {
	queue := NewWakeQueue()
	if queue.Wake("") || queue.Len() != 0 {
		t.Fatal("empty ID entered queue")
	}
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
