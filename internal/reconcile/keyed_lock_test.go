package reconcile

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestKeyedLockSingleUseIsReclaimed 验证单次临界区退出后不保留历史 ID。
func TestKeyedLockSingleUseIsReclaimed(t *testing.T) {
	locks := NewKeyedLock()
	unlock := locks.Lock("sandbox-a")
	if locks.entryCount() != 1 {
		t.Fatal("active lock entry is missing")
	}
	unlock()
	unlock()
	if locks.entryCount() != 0 {
		t.Fatalf("idle entries: %d", locks.entryCount())
	}
}

// TestKeyedLockSerializesWaitersAndReclaims 验证多个 waiter 对同一 ID 严格串行。
func TestKeyedLockSerializesWaitersAndReclaims(t *testing.T) {
	locks := NewKeyedLock()
	first := locks.Lock("sandbox-a")
	const waiters = 32
	var active, maximum atomic.Int32
	var wait sync.WaitGroup
	wait.Add(waiters)
	start := make(chan struct{})
	for range waiters {
		go func() {
			defer wait.Done()
			<-start
			unlock := locks.Lock("sandbox-a")
			current := active.Add(1)
			if current > maximum.Load() {
				maximum.Store(current)
			}
			active.Add(-1)
			unlock()
		}()
	}
	close(start)
	first()
	wait.Wait()
	if maximum.Load() != 1 || locks.entryCount() != 0 {
		t.Fatalf("maximum=%d entries=%d", maximum.Load(), locks.entryCount())
	}
}

// TestKeyedLockCancelledWaiterIsReclaimed 验证取消等待不影响 holder 或后续重建。
func TestKeyedLockCancelledWaiterIsReclaimed(t *testing.T) {
	locks := NewKeyedLock()
	holder := locks.Lock("sandbox-a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := locks.LockContext(ctx, "sandbox-a"); unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock returned unlock=%t err=%v", unlock != nil, err)
	}
	holder()
	if locks.entryCount() != 0 {
		t.Fatalf("cancelled waiter leaked entry: %d", locks.entryCount())
	}
	unlocked := locks.Lock("sandbox-a")
	unlocked()
	if locks.entryCount() != 0 {
		t.Fatal("rebuilt entry was not reclaimed")
	}
}

// TestKeyedLockOldReleaseCannotDeleteRebuiltEntry 验证 once 与 entry 身份共同阻止 ABA 误删。
func TestKeyedLockOldReleaseCannotDeleteRebuiltEntry(t *testing.T) {
	locks := NewKeyedLock()
	oldRelease := locks.Lock("sandbox-a")
	oldRelease()
	newRelease := locks.Lock("sandbox-a")
	oldRelease()
	if locks.entryCount() != 1 {
		t.Fatal("old release deleted rebuilt entry")
	}
	newRelease()
}

// TestKeyedLockManyHistoricalIDsReturnToZero 验证大量历史 sandbox 不扩张 map。
func TestKeyedLockManyHistoricalIDsReturnToZero(t *testing.T) {
	locks := NewKeyedLock()
	for index := 0; index < 100_000; index++ {
		unlock := locks.Lock(string(rune(index + 1)))
		unlock()
	}
	if locks.entryCount() != 0 {
		t.Fatalf("historical entries: %d", locks.entryCount())
	}
}
