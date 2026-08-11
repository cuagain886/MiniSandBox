package reconcile

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestTTLSchedulerEmptyToDue 验证空 heap 不建 timer，新增后只建立一个并按时回调。
func TestTTLSchedulerEmptyToDue(t *testing.T) {
	start := time.Date(2028, 4, 5, 6, 7, 8, 0, time.UTC)
	clock := newManualClock(start)
	due := make(chan TTLHeapEntry, 1)
	scheduler := NewTTLScheduler(clock, func(_ context.Context, entry TTLHeapEntry) { due <- entry })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Run(ctx); close(done) }()
	select {
	case <-clock.timerCreated:
		t.Fatal("empty scheduler created timer")
	default:
	}
	scheduler.Upsert(TTLHeapEntry{SandboxID: "one", ExpectedExpiresAt: start.Add(time.Minute)})
	waitTTLTimerCreated(t, clock)
	clock.Advance(time.Minute)
	got := waitTTLDue(t, due)
	if got.SandboxID != "one" || scheduler.Len() != 0 {
		t.Fatalf("due callback/heap: %#v len=%d", got, scheduler.Len())
	}
	select {
	case <-clock.timerCreated:
		t.Fatal("scheduler created more than one timer")
	default:
	}
	cancel()
	waitTTLSchedulerStopped(t, done)
}

// TestTTLSchedulerResetsEarlierLaterAndRemoves 验证排序变化会重置共享 timer。
func TestTTLSchedulerResetsEarlierLaterAndRemoves(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	clock := newManualClock(start)
	due := make(chan TTLHeapEntry, 4)
	scheduler := NewTTLScheduler(clock, func(_ context.Context, entry TTLHeapEntry) { due <- entry })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go scheduler.Run(ctx)
	scheduler.Upsert(TTLHeapEntry{SandboxID: "target", ExpectedExpiresAt: start.Add(10 * time.Minute)})
	waitTTLTimerCreated(t, clock)
	scheduler.Upsert(TTLHeapEntry{SandboxID: "target", ExpectedExpiresAt: start.Add(2 * time.Minute)})
	clock.Advance(2 * time.Minute)
	if got := waitTTLDue(t, due); got.SandboxID != "target" {
		t.Fatalf("earlier due: %#v", got)
	}
	scheduler.Upsert(TTLHeapEntry{SandboxID: "later", ExpectedExpiresAt: start.Add(5 * time.Minute)})
	scheduler.Upsert(TTLHeapEntry{SandboxID: "removed", ExpectedExpiresAt: start.Add(3 * time.Minute)})
	scheduler.Remove("removed")
	clock.Advance(3 * time.Minute)
	if got := waitTTLDue(t, due); got.SandboxID != "later" {
		t.Fatalf("later due: %#v", got)
	}
}

// TestTTLSchedulerStableSameTimeAndSlowCallback 验证同刻顺序且 callback 不持有 heap mutex。
func TestTTLSchedulerStableSameTimeAndSlowCallback(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	clock := newManualClock(start)
	entered := make(chan struct{})
	release := make(chan struct{})
	callbacks := make(chan string, 3)
	scheduler := NewTTLScheduler(clock, func(_ context.Context, entry TTLHeapEntry) {
		if entry.SandboxID == "a" {
			close(entered)
			<-release
		}
		callbacks <- entry.SandboxID
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go scheduler.Run(ctx)
	for _, id := range []string{"c", "a", "b"} {
		scheduler.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: start.Add(time.Minute)})
	}
	waitTTLTimerCreated(t, clock)
	clock.Advance(time.Minute)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow callback was not entered")
	}
	updated := make(chan struct{})
	go func() {
		scheduler.Upsert(TTLHeapEntry{SandboxID: "new", ExpectedExpiresAt: start.Add(2 * time.Minute)})
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("slow callback held heap mutex")
	}
	close(release)
	for index, want := range []string{"a", "b", "c"} {
		select {
		case got := <-callbacks:
			if got != want {
				t.Fatalf("callback %d: got %q, want %q", index, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("callback %d missing", index)
		}
	}
}

// TestTTLSchedulerShutdownAndConcurrentReset 验证 reset race 与取消都不会遗留 loop。
func TestTTLSchedulerShutdownAndConcurrentReset(t *testing.T) {
	clock := newManualClock(time.Unix(0, 0).UTC())
	scheduler := NewTTLScheduler(clock, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Run(ctx); close(done) }()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 200; index++ {
				id := fmt.Sprintf("sandbox-%d-%d", worker, index%10)
				scheduler.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: clock.Now().Add(time.Duration(index+1) * time.Second)})
				if index%4 == 0 {
					scheduler.Remove(id)
				}
			}
		}()
	}
	wait.Wait()
	cancel()
	waitTTLSchedulerStopped(t, done)
	clock.mu.Lock()
	defer clock.mu.Unlock()
	for _, timer := range clock.timers {
		if timer.active {
			t.Fatal("shutdown left an active TTL timer")
		}
	}
}

func waitTTLTimerCreated(t *testing.T, clock *manualClock) {
	t.Helper()
	select {
	case <-clock.timerCreated:
	case <-time.After(time.Second):
		t.Fatal("TTL timer was not created")
	}
}

func waitTTLDue(t *testing.T, due <-chan TTLHeapEntry) TTLHeapEntry {
	t.Helper()
	select {
	case entry := <-due:
		return entry
	case <-time.After(time.Second):
		t.Fatal("TTL callback was not invoked")
		return TTLHeapEntry{}
	}
}

func waitTTLSchedulerStopped(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TTL scheduler did not stop")
	}
}
