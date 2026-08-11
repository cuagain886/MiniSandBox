package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	storeport "minisandbox/internal/store"
)

type fakeIdempotencyGCStore struct {
	mu      sync.Mutex
	batches []storeport.IdempotencyGCBatch
	err     error
	calls   []storeport.IdempotencyGCQuery
}

func (s *fakeIdempotencyGCStore) DeleteExpiredIdempotencyRecords(_ context.Context, query storeport.IdempotencyGCQuery) (storeport.IdempotencyGCBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, query)
	if s.err != nil {
		return storeport.IdempotencyGCBatch{}, s.err
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	return batch, nil
}

// TestIdempotencyGCSweepOnceAdvancesCursor 验证多批回收使用上一批稳定 key。
func TestIdempotencyGCSweepOnceAdvancesCursor(t *testing.T) {
	store := &fakeIdempotencyGCStore{batches: []storeport.IdempotencyGCBatch{
		{Deleted: 2, LastScopeID: "local:v1", LastKey: "b"},
		{Deleted: 1, LastScopeID: "local:v1", LastKey: "c"},
	}}
	gc, err := NewIdempotencyGC(store, 24*time.Hour, time.Hour, 2, nil)
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	fixed := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	gc.clock = newManualClock(fixed)
	deleted, err := gc.SweepOnce(context.Background())
	if err != nil || deleted != 3 || len(store.calls) != 2 {
		t.Fatalf("sweep result: deleted=%d calls=%#v err=%v", deleted, store.calls, err)
	}
	if store.calls[1].AfterScopeID != "local:v1" || store.calls[1].AfterKey != "b" || !store.calls[0].Now.Equal(fixed) {
		t.Fatalf("cursor/time: %#v", store.calls)
	}
}

// TestIdempotencyGCRunStopsAndRetries 验证失败只报告且 shutdown 能结束 loop。
func TestIdempotencyGCRunStopsAndRetries(t *testing.T) {
	injected := errors.New("injected GC failure")
	store := &fakeIdempotencyGCStore{err: injected}
	reported := make(chan error, 2)
	clock := newManualClock(time.Unix(0, 0).UTC())
	gc, err := NewIdempotencyGCWithClock(store, 24*time.Hour, time.Minute, 10, clock, func(err error) { reported <- err })
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); gc.Run(ctx) }()
	<-clock.tickerCreated
	for index := 0; index < 2; index++ {
		clock.Advance(time.Minute)
		select {
		case err := <-reported:
			if !errors.Is(err, injected) {
				t.Fatalf("reported error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("GC did not retry")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("GC did not stop")
	}
}
