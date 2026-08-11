package reconcile

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestTTLHeapUpsertFixAndRemove 验证提前、延后、替换和幂等删除保持唯一 entry。
func TestTTLHeapUpsertFixAndRemove(t *testing.T) {
	base := time.Date(2028, 3, 4, 5, 6, 7, 0, time.UTC)
	h := NewTTLHeap()
	if _, ok := h.Peek(); ok || h.Len() != 0 {
		t.Fatal("new heap is not empty")
	}
	if !h.Upsert(TTLHeapEntry{SandboxID: "b", ExpectedExpiresAt: base.Add(2 * time.Hour)}) ||
		!h.Upsert(TTLHeapEntry{SandboxID: "a", ExpectedExpiresAt: base.Add(time.Hour)}) {
		t.Fatal("initial insert reported no change")
	}
	assertTTLHeapPeek(t, h, "a", base.Add(time.Hour))
	if h.Upsert(TTLHeapEntry{SandboxID: "a", ExpectedExpiresAt: base.Add(time.Hour)}) {
		t.Fatal("same expiry changed heap")
	}
	if !h.Upsert(TTLHeapEntry{SandboxID: "a", ExpectedExpiresAt: base.Add(3 * time.Hour)}) {
		t.Fatal("later replacement reported no change")
	}
	assertTTLHeapPeek(t, h, "b", base.Add(2*time.Hour))
	if !h.Upsert(TTLHeapEntry{SandboxID: "a", ExpectedExpiresAt: base.Add(30 * time.Minute)}) {
		t.Fatal("earlier replacement reported no change")
	}
	assertTTLHeapPeek(t, h, "a", base.Add(30*time.Minute))
	if !h.Remove("a") || h.Remove("a") || h.Len() != 1 {
		t.Fatalf("idempotent remove/length failed: %d", h.Len())
	}
	assertTTLHeapPeek(t, h, "b", base.Add(2*time.Hour))
}

// TestTTLHeapStableTieBreak 验证同刻到期时按 sandbox ID 稳定排序。
func TestTTLHeapStableTieBreak(t *testing.T) {
	expiresAt := time.Date(2028, 3, 4, 5, 6, 7, 0, time.UTC)
	h := NewTTLHeap()
	for _, id := range []string{"c", "a", "b"} {
		h.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: expiresAt})
	}
	for _, want := range []string{"a", "b", "c"} {
		assertTTLHeapPeek(t, h, want, expiresAt)
		if !h.Remove(want) {
			t.Fatalf("remove %q failed", want)
		}
	}
}

// TestTTLHeapManyIDs 验证大量乱序 ID/expiry 仍逐次返回全局最早 entry。
func TestTTLHeapManyIDs(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	h := NewTTLHeap()
	const count = 1000
	for index := count - 1; index >= 0; index-- {
		id := fmt.Sprintf("sandbox-%04d", index)
		h.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: base.Add(time.Duration(index/4) * time.Second)})
	}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("sandbox-%04d", index)
		assertTTLHeapPeek(t, h, id, base.Add(time.Duration(index/4)*time.Second))
		h.Remove(id)
	}
}

// TestTTLHeapConcurrentAccess 为 race detector 覆盖并发 upsert、peek 和 remove。
func TestTTLHeapConcurrentAccess(t *testing.T) {
	h := NewTTLHeap()
	base := time.Now().UTC()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 500; index++ {
				id := fmt.Sprintf("sandbox-%02d-%03d", worker, index%50)
				h.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: base.Add(time.Duration(index) * time.Second)})
				h.Peek()
				if index%3 == 0 {
					h.Remove(id)
				}
			}
		}()
	}
	wait.Wait()
}

func assertTTLHeapPeek(t *testing.T, h *TTLHeap, wantID string, wantExpiry time.Time) {
	t.Helper()
	got, ok := h.Peek()
	if !ok || got.SandboxID != wantID || !got.ExpectedExpiresAt.Equal(wantExpiry) {
		t.Fatalf("peek: got %#v/%v, want %q/%s", got, ok, wantID, wantExpiry)
	}
}
