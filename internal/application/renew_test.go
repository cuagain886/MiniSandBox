package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type renewRecordingWaker struct {
	mu  sync.Mutex
	ids []string
}

func (w *renewRecordingWaker) Wake(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids = append(w.ids, id)
}

func (w *renewRecordingWaker) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.ids...)
}

type renewTestStore struct {
	storeport.Store
	records      []domain.Sandbox
	getIndex     int
	renewResults []error
	renewCalls   []storeport.RenewUpdate
}

func (s *renewTestStore) Get(context.Context, string) (domain.Sandbox, error) {
	index := s.getIndex
	if index >= len(s.records) {
		index = len(s.records) - 1
	}
	s.getIndex++
	return s.records[index], nil
}

func (s *renewTestStore) Renew(_ context.Context, update storeport.RenewUpdate) (domain.Sandbox, error) {
	s.renewCalls = append(s.renewCalls, update)
	index := len(s.renewCalls) - 1
	if index < len(s.renewResults) && s.renewResults[index] != nil {
		return domain.Sandbox{}, s.renewResults[index]
	}
	result := s.records[len(s.records)-1]
	expiresAt := update.ExpiresAt
	result.ExpiresAt = &expiresAt
	result.Revision++
	return result, nil
}

// TestRenewSandboxSuccess 验证成功路径携带当前 revision、服务端 now 和 UTC expiry。
func TestRenewSandboxSuccess(t *testing.T) {
	now := time.Date(2028, 7, 8, 9, 10, 11, 0, time.UTC)
	currentExpiry := now.Add(time.Hour)
	requested := now.Add(2 * time.Hour)
	record := activeRenewRecord("renew-ok", currentExpiry, 12)
	store := &renewTestStore{records: []domain.Sandbox{record}}
	service := renewServiceForTest(store, now)
	waker := &renewRecordingWaker{}
	service.waker = waker
	got, err := service.Renew(context.Background(), RenewSandbox{SandboxID: record.ID, ExpiresAt: requested})
	if err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(requested) || len(store.renewCalls) != 1 {
		t.Fatalf("renew: got=%#v err=%v calls=%#v", got, err, store.renewCalls)
	}
	call := store.renewCalls[0]
	if call.ExpectedRevision != 12 || !call.Now.Equal(now) || !call.ExpiresAt.Equal(requested) {
		t.Fatalf("renew call: %#v", call)
	}
	if got := waker.snapshot(); len(got) != 1 || got[0] != record.ID {
		t.Fatalf("renew wake: %v", got)
	}
}

// TestRenewSandboxEqualExpiryDoesNotWake 验证相同 expiry 是真正 no-op，不制造 revision 或 reconcile 噪声。
func TestRenewSandboxEqualExpiryDoesNotWake(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(time.Hour)
	store := &renewTestStore{records: []domain.Sandbox{activeRenewRecord("renew-noop", expiresAt, 3)}}
	waker := &renewRecordingWaker{}
	service := renewServiceForTest(store, now)
	service.waker = waker

	got, err := service.Renew(context.Background(), RenewSandbox{SandboxID: "renew-noop", ExpiresAt: expiresAt})
	if err != nil || got.Revision != 3 || len(store.renewCalls) != 0 || len(waker.snapshot()) != 0 {
		t.Fatalf("equal renew: got=%#v err=%v writes=%d wakes=%v", got, err, len(store.renewCalls), waker.snapshot())
	}
}

// TestRenewSandboxResolvesConcurrentExpiry 验证竞争者的相同、更晚租约及删除意图优先。
func TestRenewSandboxResolvesConcurrentExpiry(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	oldExpiry := now.Add(time.Hour)
	requested := now.Add(2 * time.Hour)
	tests := []struct {
		name      string
		competing domain.Sandbox
		wantErr   error
	}{
		{name: "same value becomes no-op", competing: activeRenewRecord("renew-race", requested, 2)},
		{name: "later value wins", competing: activeRenewRecord("renew-race", requested.Add(time.Hour), 2), wantErr: domain.ErrLeaseConflict},
		{name: "delete wins", competing: func() domain.Sandbox {
			record := activeRenewRecord("renew-race", oldExpiry, 2)
			record.DesiredState = domain.DesiredTerminated
			return record
		}(), wantErr: domain.ErrSandboxExpiring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &renewTestStore{
				records:      []domain.Sandbox{activeRenewRecord("renew-race", oldExpiry, 1), tt.competing},
				renewResults: []error{domain.ErrConflict},
			}
			got, err := renewServiceForTest(store, now).Renew(context.Background(), RenewSandbox{SandboxID: "renew-race", ExpiresAt: requested})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("race result: got=%#v err=%v", got, err)
			}
			if tt.wantErr == nil && (got.ExpiresAt == nil || !got.ExpiresAt.Equal(requested)) {
				t.Fatalf("same-value no-op: %#v", got)
			}
			if len(store.renewCalls) != 1 {
				t.Fatalf("race performed %d writes", len(store.renewCalls))
			}
		})
	}
}

// TestRenewSandboxBoundsCASRetries 验证持续冲突最多执行固定次数。
func TestRenewSandboxBoundsCASRetries(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	oldExpiry := now.Add(time.Hour)
	records := make([]domain.Sandbox, renewCASAttempts)
	for index := range records {
		records[index] = activeRenewRecord("renew-hot", oldExpiry, uint64(index+1))
	}
	store := &renewTestStore{records: records, renewResults: []error{domain.ErrConflict, domain.ErrConflict, domain.ErrConflict}}
	_, err := renewServiceForTest(store, now).Renew(context.Background(), RenewSandbox{SandboxID: "renew-hot", ExpiresAt: now.Add(2 * time.Hour)})
	if !errors.Is(err, domain.ErrConflict) || len(store.renewCalls) != renewCASAttempts || store.getIndex != renewCASAttempts {
		t.Fatalf("bounded retry: err=%v writes=%d reads=%d", err, len(store.renewCalls), store.getIndex)
	}
}

func renewServiceForTest(store storeport.Store, now time.Time) *SandboxService {
	return &SandboxService{
		store: store, clock: &recordingClock{now: now},
		createPolicy: CreatePolicy{MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour},
	}
}

func activeRenewRecord(id string, expiresAt time.Time, revision uint64) domain.Sandbox {
	return domain.Sandbox{
		ID: id, DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning,
		ExpiresAt: &expiresAt, Revision: revision,
	}
}
