package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/internal/testutil"
)

// TestTTLDueValidatorRejectsRenewedOldEntry 验证续期后的旧 timer 失效且新 expiry 被重排。
func TestTTLDueValidatorRejectsRenewedOldEntry(t *testing.T) {
	now := time.Date(2028, 5, 6, 7, 8, 9, 0, time.UTC)
	oldExpiry := now.Add(-time.Minute)
	newExpiry := now.Add(time.Hour)
	record := activeTTLRecord("renewed", newExpiry, 9)
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(record, nil)
	index := NewTTLHeap()
	validator := NewTTLDueValidator(storeFake, index, newManualClock(now))

	result, ok, err := validator.Validate(context.Background(), TTLHeapEntry{
		SandboxID: record.ID, ExpectedExpiresAt: oldExpiry,
	})
	if err != nil || ok || result != (ValidatedTTLDue{}) {
		t.Fatalf("stale validation: %#v/%v/%v", result, ok, err)
	}
	assertTTLHeapPeek(t, index, record.ID, newExpiry)
}

// TestTTLDueValidatorIgnoresUnrelatedRevisionChange 验证 expiry 相同即可继续，不绑定 revision。
func TestTTLDueValidatorIgnoresUnrelatedRevisionChange(t *testing.T) {
	now := time.Date(2028, 5, 6, 7, 8, 9, 0, time.UTC)
	expiresAt := now.Add(-time.Second)
	record := activeTTLRecord("observed-updated", expiresAt, 77)
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(record, nil)
	index := NewTTLHeap()
	validator := NewTTLDueValidator(storeFake, index, newManualClock(now))

	result, ok, err := validator.Validate(context.Background(), TTLHeapEntry{
		SandboxID: record.ID, ExpectedExpiresAt: expiresAt,
	})
	if err != nil || !ok || result.Sandbox.Revision != 77 || !result.CheckedAt.Equal(now) || index.Len() != 0 {
		t.Fatalf("current validation: %#v/%v/%v len=%d", result, ok, err, index.Len())
	}
}

// TestTTLDueValidatorReschedulesEarlyTimer 验证当前时间尚早时保留同一有效租约。
func TestTTLDueValidatorReschedulesEarlyTimer(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	expiresAt := now.Add(time.Minute)
	record := activeTTLRecord("early", expiresAt, 2)
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(record, nil)
	index := NewTTLHeap()
	validator := NewTTLDueValidator(storeFake, index, newManualClock(now))
	_, ok, err := validator.Validate(context.Background(), TTLHeapEntry{
		SandboxID: record.ID, ExpectedExpiresAt: expiresAt,
	})
	if err != nil || ok {
		t.Fatalf("early validation: ok=%v err=%v", ok, err)
	}
	assertTTLHeapPeek(t, index, record.ID, expiresAt)
}

// TestTTLDueValidatorRemovesDeletedOrMissing 验证终止记录和不存在记录均清除内存 entry。
func TestTTLDueValidatorRemovesDeletedOrMissing(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	expiresAt := now.Add(-time.Second)
	tests := []struct {
		name   string
		record domain.Sandbox
		err    error
	}{
		{name: "not found", err: domain.ErrNotFound},
		{name: "desired terminated", record: func() domain.Sandbox {
			record := activeTTLRecord("deleted", expiresAt, 3)
			record.DesiredState = domain.DesiredTerminated
			return record
		}()},
		{name: "observed terminated", record: func() domain.Sandbox {
			record := activeTTLRecord("terminated", expiresAt, 4)
			record.ObservedState = domain.StateTerminated
			return record
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.record.ID
			if id == "" {
				id = "missing"
			}
			storeFake := testutil.NewFakeStore()
			storeFake.SetGetResult(tt.record, tt.err)
			index := NewTTLHeap()
			index.Upsert(TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: expiresAt})
			validator := NewTTLDueValidator(storeFake, index, newManualClock(now))
			_, ok, err := validator.Validate(context.Background(), TTLHeapEntry{SandboxID: id, ExpectedExpiresAt: expiresAt})
			if err != nil || ok || index.Len() != 0 {
				t.Fatalf("terminal validation: ok=%v err=%v len=%d", ok, err, index.Len())
			}
		})
	}
}

// TestTTLDueValidatorLeavesStoreErrorForRecovery 验证读取失败不伪造到期或新租约。
func TestTTLDueValidatorLeavesStoreErrorForRecovery(t *testing.T) {
	injected := errors.New("store unavailable")
	storeFake := testutil.NewFakeStore()
	storeFake.SetGetResult(domain.Sandbox{}, injected)
	index := NewTTLHeap()
	validator := NewTTLDueValidator(storeFake, index, newManualClock(time.Unix(0, 0).UTC()))
	_, ok, err := validator.Validate(context.Background(), TTLHeapEntry{
		SandboxID: "store-error", ExpectedExpiresAt: time.Unix(-1, 0).UTC(),
	})
	if !errors.Is(err, injected) || ok || index.Len() != 0 {
		t.Fatalf("Store error validation: ok=%v err=%v len=%d", ok, err, index.Len())
	}
}

// TestTTLDueValidatorRejectsMissingExpiry 验证损坏记录不会进入 expire CAS。
func TestTTLDueValidatorRejectsMissingExpiry(t *testing.T) {
	storeFake := testutil.NewFakeStore()
	record := activeTTLRecord("corrupt", time.Unix(1, 0).UTC(), 1)
	record.ExpiresAt = nil
	storeFake.SetGetResult(record, nil)
	index := NewTTLHeap()
	validator := NewTTLDueValidator(storeFake, index, newManualClock(time.Unix(2, 0).UTC()))
	_, ok, err := validator.Validate(context.Background(), TTLHeapEntry{
		SandboxID: record.ID, ExpectedExpiresAt: time.Unix(1, 0).UTC(),
	})
	if !errors.Is(err, storeport.ErrCorrupt) || ok {
		t.Fatalf("missing expiry: ok=%v err=%v", ok, err)
	}
}

func activeTTLRecord(id string, expiresAt time.Time, revision uint64) domain.Sandbox {
	return domain.Sandbox{
		ID: id, DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning,
		ExpiresAt: &expiresAt, Revision: revision,
	}
}
