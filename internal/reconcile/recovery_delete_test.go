package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type recoveryDeleteTestStore struct {
	record    domain.Sandbox
	gets      int
	updates   []storeport.ObservedUpdate
	conflicts int
}

func (s *recoveryDeleteTestStore) Get(context.Context, string) (domain.Sandbox, error) {
	s.gets++
	return s.record, nil
}

func (s *recoveryDeleteTestStore) UpdateObserved(_ context.Context, update storeport.ObservedUpdate) (domain.Sandbox, error) {
	s.updates = append(s.updates, update)
	if s.conflicts > 0 {
		s.conflicts--
		s.record.Revision++
		return domain.Sandbox{}, domain.ErrConflict
	}
	s.record.RuntimeID = update.RuntimeID
	s.record.NextReconcileAt = update.ReconcileAt
	s.record.Revision++
	return s.record, nil
}

// TestRecoveryDeleteExecutorWakesCompleteAndPartialResiduals 验证完整或部分可信残留都进入普通删除。
func TestRecoveryDeleteExecutorWakesCompleteAndPartialResiduals(t *testing.T) {
	for _, partial := range []bool{false, true} {
		stored, actual, plan, now := recoveryDeleteFixture(domain.StateStopping)
		if partial {
			actual.Workspace, actual.Directory = nil, nil
		}
		store := &recoveryDeleteTestStore{record: stored}
		wakes := 0
		executor := mustRecoveryDeleteExecutor(t, store, now, func(string) bool { wakes++; return true })
		updated, err := executor.Execute(context.Background(), plan, actual)
		if err != nil || wakes != 1 || len(store.updates) != 1 || updated.NextReconcileAt == nil || !updated.NextReconcileAt.Equal(now) {
			t.Fatalf("partial=%t updated=%#v wakes=%d updates=%#v err=%v", partial, updated, wakes, store.updates, err)
		}
		if updated.DesiredState != domain.DesiredTerminated || updated.ObservedState != domain.StateStopping {
			t.Fatalf("delete intent resurrected: %#v", updated)
		}
	}
}

// TestRecoveryDeleteExecutorHandlesTerminatedResidualAndFutureBackoff 验证 Terminated 残留同样只提前 backoff，不改回 Running。
func TestRecoveryDeleteExecutorHandlesTerminatedResidualAndFutureBackoff(t *testing.T) {
	stored, actual, plan, now := recoveryDeleteFixture(domain.StateTerminated)
	store := &recoveryDeleteTestStore{record: stored}
	executor := mustRecoveryDeleteExecutor(t, store, now, func(string) bool { return true })
	updated, err := executor.Execute(context.Background(), plan, actual)
	if err != nil || updated.ObservedState != domain.StateTerminated || updated.DesiredState != domain.DesiredTerminated || updated.RuntimeID != actual.Main.ContainerID {
		t.Fatalf("terminated residual: %#v/%v", updated, err)
	}
}

// TestRecoveryDeleteExecutorIsIdempotentForDueRecord 验证重复启动只 Wake，不产生重复 Store 写入。
func TestRecoveryDeleteExecutorIsIdempotentForDueRecord(t *testing.T) {
	stored, actual, plan, now := recoveryDeleteFixture(domain.StateStopping)
	due := now.Add(-time.Second)
	stored.NextReconcileAt = &due
	store := &recoveryDeleteTestStore{record: stored}
	wakes := 0
	executor := mustRecoveryDeleteExecutor(t, store, now, func(string) bool { wakes++; return true })
	for range 2 {
		if _, err := executor.Execute(context.Background(), plan, actual); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.updates) != 0 || wakes != 2 {
		t.Fatalf("idempotence: updates=%d wakes=%d", len(store.updates), wakes)
	}
}

// TestRecoveryDeleteExecutorRereadsConflictAndRejectsResurrection 验证 CAS 重读以及 desired 变化时停止。
func TestRecoveryDeleteExecutorRereadsConflictAndRejectsResurrection(t *testing.T) {
	stored, actual, plan, now := recoveryDeleteFixture(domain.StateStopping)
	store := &recoveryDeleteTestStore{record: stored, conflicts: 1}
	executor := mustRecoveryDeleteExecutor(t, store, now, func(string) bool { return true })
	if _, err := executor.Execute(context.Background(), plan, actual); err != nil || store.gets != 2 || len(store.updates) != 2 {
		t.Fatalf("conflict retry: gets=%d updates=%d err=%v", store.gets, len(store.updates), err)
	}
	store.record.DesiredState = domain.DesiredRunning
	if _, err := executor.Execute(context.Background(), plan, actual); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("resurrection accepted: %v", err)
	}
}

func recoveryDeleteFixture(state domain.SandboxState) (domain.Sandbox, ActualResourceSnapshot, RecoveryPlan, time.Time) {
	now := time.Unix(100, 0).UTC()
	stored := recoveryPlanSandbox(domain.DesiredTerminated, state)
	future := now.Add(time.Hour)
	stored.NextReconcileAt = &future
	actual := recoveryPlanActual()
	plan := PlanRecovery(&stored, &actual)
	return stored, actual, plan, now
}

func mustRecoveryDeleteExecutor(t *testing.T, store RecoveryDeleteStore, now time.Time, wake RecoveryWake) *RecoveryDeleteExecutor {
	t.Helper()
	executor, err := NewRecoveryDeleteExecutor(store, newManualClock(now), wake)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}
