package docker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEgressAttachLockSerializesPerSandbox 验证同一 sandbox 的 attach 请求严格串行，
// 完成后引用计数会删除锁，避免 sandbox churn 造成无界内存增长。
func TestEgressAttachLockSerializesPerSandbox(t *testing.T) {
	runtime := &Runtime{}
	var active atomic.Int32
	var maximum atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			unlock := runtime.lockEgressAttach("sandbox-a")
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			active.Add(-1)
			unlock()
		}()
	}
	wait.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("same-sandbox attach concurrency = %d, want 1", maximum.Load())
	}
	runtime.egressLocksMu.Lock()
	defer runtime.egressLocksMu.Unlock()
	if len(runtime.egressLocks) != 0 {
		t.Fatalf("released egress locks retained: %d", len(runtime.egressLocks))
	}
}

// TestEgressAttachLockAllowsDifferentSandboxes 验证一个 sandbox 的慢查询不会阻塞
// 其他 sandbox 的独立控制通道。
func TestEgressAttachLockAllowsDifferentSandboxes(t *testing.T) {
	runtime := &Runtime{}
	unlockA := runtime.lockEgressAttach("sandbox-a")
	defer unlockA()
	acquiredB := make(chan struct{})
	go func() {
		unlockB := runtime.lockEgressAttach("sandbox-b")
		close(acquiredB)
		unlockB()
	}()
	select {
	case <-acquiredB:
	case <-time.After(time.Second):
		t.Fatal("independent sandbox attach was blocked")
	}
}
