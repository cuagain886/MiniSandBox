package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type expirationTestStore struct {
	mu          sync.Mutex
	record      domain.Sandbox
	getCalls    int
	expireCalls []storeport.ExpireIntentUpdate
	conflicts   int
}

func (s *expirationTestStore) Get(context.Context, string) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	return s.record, nil
}

func (s *expirationTestStore) ExpireIntent(_ context.Context, update storeport.ExpireIntentUpdate) (domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireCalls = append(s.expireCalls, update)
	if s.conflicts > 0 {
		s.conflicts--
		s.record.Revision++
		return domain.Sandbox{}, domain.ErrConflict
	}
	s.record.DesiredState = domain.DesiredTerminated
	s.record.ObservedState = domain.StateStopping
	s.record.Reason = domain.SandboxReasonTTLExpired
	s.record.Revision++
	return s.record, nil
}

// TestTTLExpirationCoordinatorSubmitsAllActiveObservedStates 验证到期策略不依赖中间 observed 状态。
func TestTTLExpirationCoordinatorSubmitsAllActiveObservedStates(t *testing.T) {
	for _, state := range []domain.SandboxState{domain.StatePending, domain.StateCreating, domain.StateRunning, domain.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			now := time.Unix(100, 0).UTC()
			expiresAt := now.Add(-time.Second)
			record := activeTTLRecord("expire-active", expiresAt, 4)
			record.ObservedState = state
			store := &expirationTestStore{record: record}
			index := NewTTLHeap()
			index.Upsert(TTLHeapEntry{SandboxID: record.ID, ExpectedExpiresAt: expiresAt})
			var wakes int
			coordinator := NewTTLExpirationCoordinator(store, index, newManualClock(now), func(id string) bool {
				wakes++
				return id == record.ID
			})
			if err := coordinator.ExpireEntry(context.Background(), TTLHeapEntry{SandboxID: record.ID, ExpectedExpiresAt: expiresAt}); err != nil {
				t.Fatalf("expire: %v", err)
			}
			if len(store.expireCalls) != 1 || store.record.DesiredState != domain.DesiredTerminated ||
				store.record.Reason != domain.SandboxReasonTTLExpired || index.Len() != 0 || wakes != 1 {
				t.Fatalf("result: store=%#v calls=%#v len=%d wakes=%d", store.record, store.expireCalls, index.Len(), wakes)
			}
		})
	}
}

// TestTTLExpirationCoordinatorRereadsCASConflict 验证无关 revision 冲突后重读并提交。
func TestTTLExpirationCoordinatorRereadsCASConflict(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(-time.Second)
	store := &expirationTestStore{record: activeTTLRecord("cas", expiresAt, 7), conflicts: 1}
	coordinator := NewTTLExpirationCoordinator(store, NewTTLHeap(), newManualClock(now), nil)
	if err := coordinator.ExpireEntry(context.Background(), TTLHeapEntry{SandboxID: "cas", ExpectedExpiresAt: expiresAt}); err != nil {
		t.Fatalf("expire after conflict: %v", err)
	}
	if store.getCalls != 2 || len(store.expireCalls) != 2 || store.expireCalls[1].ExpectedRevision != 8 {
		t.Fatalf("retry calls: gets=%d expires=%#v", store.getCalls, store.expireCalls)
	}
}

// TestTTLExpirationCoordinatorIsIdempotentAndIgnoresWakeFailure 验证已删除意图与关闭队列均不改写成功。
func TestTTLExpirationCoordinatorIsIdempotentAndIgnoresWakeFailure(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(-time.Second)
	store := &expirationTestStore{record: activeTTLRecord("repeat", expiresAt, 1)}
	coordinator := NewTTLExpirationCoordinator(store, NewTTLHeap(), newManualClock(now), func(string) bool { return false })
	entry := TTLHeapEntry{SandboxID: "repeat", ExpectedExpiresAt: expiresAt}
	if err := coordinator.ExpireEntry(context.Background(), entry); err != nil {
		t.Fatalf("first expire: %v", err)
	}
	if err := coordinator.ExpireEntry(context.Background(), entry); err != nil {
		t.Fatalf("repeated expire: %v", err)
	}
	if len(store.expireCalls) != 1 {
		t.Fatalf("repeated callback wrote %d intents", len(store.expireCalls))
	}
}

// TestTTLExpirationCoordinatorBoundsConflicts 验证持续竞争不会无界自旋。
func TestTTLExpirationCoordinatorBoundsConflicts(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(-time.Second)
	store := &expirationTestStore{record: activeTTLRecord("hot", expiresAt, 1), conflicts: ttlExpireCASAttempts}
	err := NewTTLExpirationCoordinator(store, NewTTLHeap(), newManualClock(now), nil).
		ExpireEntry(context.Background(), TTLHeapEntry{SandboxID: "hot", ExpectedExpiresAt: expiresAt})
	if !errors.Is(err, domain.ErrConflict) || len(store.expireCalls) != ttlExpireCASAttempts {
		t.Fatalf("bounded conflicts: err=%v calls=%d", err, len(store.expireCalls))
	}
}
