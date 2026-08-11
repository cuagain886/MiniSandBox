package reconcile

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type ttlRecoveryTestStore struct {
	mu      sync.Mutex
	records map[string]domain.Sandbox
	writes  []storeport.ExpireIntentUpdate
}

func (s *ttlRecoveryTestStore) Get(_ context.Context, id string) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	return record, nil
}

func (s *ttlRecoveryTestStore) ExpireIntent(_ context.Context, update storeport.ExpireIntentUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[update.ID]
	if record.Revision != update.ExpectedRevision || record.ExpiresAt == nil || !record.ExpiresAt.Equal(update.ExpectedExpiresAt) {
		return domain.Sandbox{}, domain.ErrConflict
	}
	record.DesiredState, record.ObservedState = domain.DesiredTerminated, domain.StateStopping
	record.Reason, record.Message = domain.SandboxReasonTTLExpired, "Sandbox lease has expired."
	record.Revision++
	s.records[update.ID] = record
	s.writes = append(s.writes, update)
	return record, nil
}

func (s *ttlRecoveryTestStore) ListActiveLeases(_ context.Context, afterID string, limit int) ([]domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.records))
	for id, record := range s.records {
		if id > afterID && record.DesiredState == domain.DesiredRunning && record.ObservedState != domain.StateTerminated {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	result := make([]domain.Sandbox, 0, len(ids))
	for _, id := range ids {
		result = append(result, s.records[id])
	}
	return result, nil
}

// TestTTLRecoveryRebuildsPagedHeapBeforeTimer 验证重启按 keyset 恢复全部未来租约。
func TestTTLRecoveryRebuildsPagedHeapBeforeTimer(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	clock := newManualClock(now)
	store := &ttlRecoveryTestStore{records: map[string]domain.Sandbox{}}
	for index, id := range []string{"lease-a", "lease-b", "lease-c"} {
		expiresAt := now.Add(time.Duration(index+1) * time.Hour)
		store.records[id] = activeTTLRecord(id, expiresAt, 1)
	}
	scheduler := NewTTLScheduler(clock, nil)
	expiration := NewTTLExpirationCoordinator(store, scheduler, clock, nil)
	recovery, err := NewTTLRecovery(store, scheduler, expiration, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.Recover(context.Background()); err != nil || scheduler.Len() != 3 {
		t.Fatalf("recover: err=%v len=%d", err, scheduler.Len())
	}
	select {
	case <-clock.timerCreated:
		t.Fatal("recovery started timer loop before caller")
	default:
	}
}

// TestTTLRecoveryExpiresDueRecordAndFutureTimerStillFires 验证恢复期间到期与未来 timer 共享 coordinator。
func TestTTLRecoveryExpiresDueRecordAndFutureTimerStillFires(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	clock := newManualClock(now)
	dueExpiry, futureExpiry := now.Add(-time.Second), now.Add(time.Minute)
	store := &ttlRecoveryTestStore{records: map[string]domain.Sandbox{
		"lease-due":    activeTTLRecord("lease-due", dueExpiry, 1),
		"lease-future": activeTTLRecord("lease-future", futureExpiry, 1),
	}}
	wakes := make(chan string, 2)
	var expiration *TTLExpirationCoordinator
	scheduler := NewTTLScheduler(clock, func(ctx context.Context, entry TTLHeapEntry) { _ = expiration.ExpireEntry(ctx, entry) })
	expiration = NewTTLExpirationCoordinator(store, scheduler, clock, func(id string) bool { wakes <- id; return true })
	recovery, _ := NewTTLRecovery(store, scheduler, expiration, 2, 3)
	if err := recovery.Recover(context.Background()); err != nil || scheduler.Len() != 1 {
		t.Fatalf("recover: %v len=%d", err, scheduler.Len())
	}
	if got := <-wakes; got != "lease-due" {
		t.Fatalf("first wake: %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Run(ctx); close(done) }()
	waitTTLTimerCreated(t, clock)
	clock.Advance(time.Minute)
	select {
	case got := <-wakes:
		if got != "lease-future" {
			t.Fatalf("future wake: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("future recovered lease did not expire")
	}
	cancel()
	waitTTLSchedulerStopped(t, done)
}
