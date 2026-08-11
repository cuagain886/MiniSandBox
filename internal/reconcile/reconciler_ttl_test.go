package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// TestReconcileExpiresWithoutHeap 验证仅周期 Reconcile 也会把到期记录送入普通删除路径。
func TestReconcileExpiresWithoutHeap(t *testing.T) {
	events := []string{}
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(-time.Second)
	sandbox := pendingSandbox()
	sandbox.ExpiresAt = &expiresAt
	sandbox.RuntimeID = "runtime-expired"
	store := newReconcileStore(&events, sandbox)
	runtime := &recordingRuntime{events: &events}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	reconciler.clock = newManualClock(now)

	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("reconcile expired: %v", err)
	}
	want := []string{
		"store-get", "store-expire-intent", "store-update-Stopping-DELETING_RUNTIME",
		"runtime-delete", "store-update-Terminated-TERMINATED",
	}
	if !reflect.DeepEqual(events, want) || len(store.expireCalls) != 1 || store.record.ObservedState != domain.StateTerminated {
		t.Fatalf("events=%v expire=%#v record=%#v", events, store.expireCalls, store.record)
	}
}

// TestReconcileTTLExpiryConflictLeavesRuntimeUntouched 验证 CAS 冲突交给下一轮扫描重读。
func TestReconcileTTLExpiryConflictLeavesRuntimeUntouched(t *testing.T) {
	events := []string{}
	now := time.Unix(100, 0).UTC()
	expiresAt := now.Add(-time.Second)
	sandbox := pendingSandbox()
	sandbox.ExpiresAt = &expiresAt
	store := newReconcileStore(&events, sandbox)
	store.expireErr = domain.ErrConflict
	runtime := &recordingRuntime{events: &events}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	reconciler.clock = newManualClock(now)

	err := reconciler.Reconcile(context.Background(), sandbox.ID)
	if !errors.Is(err, domain.ErrConflict) || len(store.expireCalls) != 1 || !reflect.DeepEqual(events, []string{"store-get"}) {
		t.Fatalf("conflict: err=%v expires=%d runtime=%#v", err, len(store.expireCalls), runtime)
	}
}

// TestReconcileUsesFreshLeaseAfterStaleScanSnapshot 验证 scanner 旧页不绕过锁内 Store 重读。
func TestReconcileUsesFreshLeaseAfterStaleScanSnapshot(t *testing.T) {
	events := []string{}
	now := time.Unix(100, 0).UTC()
	newExpiry := now.Add(time.Hour)
	sandbox := pendingSandbox()
	sandbox.ExpiresAt = &newExpiry
	sandbox.ObservedState = domain.StateRunning
	sandbox.Reason = domain.SandboxReasonRunning
	store := newReconcileStore(&events, sandbox)
	runtime := &recordingRuntime{events: &events}
	reconciler := New(store, runtime, &recordingProbe{events: &events})
	reconciler.clock = newManualClock(now)
	if err := reconciler.Reconcile(context.Background(), sandbox.ID); err != nil {
		t.Fatalf("reconcile renewed record: %v", err)
	}
	if len(store.expireCalls) != 0 || !reflect.DeepEqual(events, []string{"store-get"}) {
		t.Fatalf("renewed record expired: calls=%#v events=%v", store.expireCalls, events)
	}
}
