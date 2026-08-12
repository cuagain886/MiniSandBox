package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type metadataRepairTestStore struct {
	record      domain.Sandbox
	getCalls    int
	updates     []storeport.ObservedUpdate
	conflicts   int
	updateError error
}

func (s *metadataRepairTestStore) Get(context.Context, string) (domain.Sandbox, error) {
	s.getCalls++
	return s.record, nil
}

func (s *metadataRepairTestStore) UpdateObserved(_ context.Context, update storeport.ObservedUpdate) (domain.Sandbox, error) {
	s.updates = append(s.updates, update)
	if s.updateError != nil {
		return domain.Sandbox{}, s.updateError
	}
	if s.conflicts > 0 {
		s.conflicts--
		s.record.Revision++
		return domain.Sandbox{}, domain.ErrConflict
	}
	if update.ExpectedRevision != s.record.Revision {
		return domain.Sandbox{}, domain.ErrConflict
	}
	s.record.RuntimeID = update.RuntimeID
	s.record.Revision++
	return s.record, nil
}

// TestMetadataRepairRepairsMissingAndOldIdentity 验证缺失或旧 RuntimeID 均只做一次 CAS 并 Wake。
func TestMetadataRepairRepairsMissingAndOldIdentity(t *testing.T) {
	for _, oldID := range []string{"", "old-main"} {
		t.Run(oldID, func(t *testing.T) {
			stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateRunning)
			stored.RuntimeID = oldID
			actual := recoveryPlanActual()
			plan := PlanRecovery(&stored, &actual)
			store := &metadataRepairTestStore{record: stored}
			var wakes []string
			executor := mustMetadataRepairExecutor(t, store, func(id string) bool { wakes = append(wakes, id); return true })
			updated, err := executor.Execute(context.Background(), plan, actual)
			if err != nil || updated.RuntimeID != "main" || len(store.updates) != 1 || !reflect.DeepEqual(wakes, []string{stored.ID}) {
				t.Fatalf("repair: updated=%#v calls=%#v wakes=%v err=%v", updated, store.updates, wakes, err)
			}
		})
	}
}

// TestMetadataRepairSameIdentityIsIdempotent 验证相同 ID 不重复写 Store 或制造 runtime。
func TestMetadataRepairSameIdentityIsIdempotent(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateCreating)
	stored.RuntimeID = "old"
	actual := recoveryPlanActual()
	plan := PlanRecovery(&stored, &actual)
	store := &metadataRepairTestStore{record: stored}
	executor := mustMetadataRepairExecutor(t, store, func(string) bool { return true })
	if _, err := executor.Execute(context.Background(), plan, actual); err != nil {
		t.Fatal(err)
	}
	plan = PlanRecovery(&store.record, &actual)
	if plan.Action != RecoveryActionWake {
		t.Fatalf("second plan: %#v", plan)
	}
	// 直接重复原 repair 计划也只能命中已一致分支，不能产生第二次 CAS。
	if _, err := executor.Execute(context.Background(), RecoveryPlan{SandboxID: stored.ID, Action: RecoveryActionRepairMetadata, ExpectedSpecHash: stored.SpecHash, ExpectedRuntimeID: "main"}, actual); err != nil {
		t.Fatal(err)
	}
	if len(store.updates) != 1 {
		t.Fatalf("duplicate updates: %#v", store.updates)
	}
}

// TestMetadataRepairRereadsCASConflict 验证冲突后使用新 revision 重读并重试。
func TestMetadataRepairRereadsCASConflict(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateCreating)
	stored.RuntimeID = "old"
	actual := recoveryPlanActual()
	store := &metadataRepairTestStore{record: stored, conflicts: 1}
	executor := mustMetadataRepairExecutor(t, store, func(string) bool { return true })
	if _, err := executor.Execute(context.Background(), PlanRecovery(&stored, &actual), actual); err != nil {
		t.Fatal(err)
	}
	if store.getCalls != 2 || len(store.updates) != 2 || store.updates[1].ExpectedRevision != stored.Revision+1 {
		t.Fatalf("conflict retry: gets=%d updates=%#v", store.getCalls, store.updates)
	}
}

// TestMetadataRepairPreservesDesiredDeleteSpecAndExpiry 验证删除目标只补 identity，不改变权威字段。
func TestMetadataRepairPreservesDesiredDeleteSpecAndExpiry(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredTerminated, domain.StateTerminated)
	stored.RuntimeID = ""
	expires := time.Unix(500, 0).UTC()
	stored.ExpiresAt = &expires
	before := stored
	actual := recoveryPlanActual()
	store := &metadataRepairTestStore{record: stored}
	executor := mustMetadataRepairExecutor(t, store, func(string) bool { return true })
	updated, err := executor.Execute(context.Background(), PlanRecovery(&stored, &actual), actual)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredState != before.DesiredState || updated.ObservedState != before.ObservedState || updated.SpecHash != before.SpecHash || !updated.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Fatalf("authority changed: before=%#v after=%#v", before, updated)
	}
}

// TestMetadataRepairRejectsDifferentAndStaleIdentity 验证 ID/spec/角色/协议/netns 与陈旧 observation 均不能写入。
func TestMetadataRepairRejectsDifferentAndStaleIdentity(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateCreating)
	stored.RuntimeID = "old"
	actual := recoveryPlanActual()
	plan := PlanRecovery(&stored, &actual)
	tests := []struct {
		name   string
		mutate func(*domain.Sandbox, *ActualResourceSnapshot)
	}{
		{"different store ID", func(s *domain.Sandbox, _ *ActualResourceSnapshot) { s.ID = "different" }},
		{"stale container", func(_ *domain.Sandbox, a *ActualResourceSnapshot) { a.Main.ContainerID = "new-main" }},
		{"changed spec", func(s *domain.Sandbox, _ *ActualResourceSnapshot) { s.SpecHash = "changed" }},
		{"wrong role", func(_ *domain.Sandbox, a *ActualResourceSnapshot) { a.Main.Role = "egress-sidecar" }},
		{"bad protocol", func(_ *domain.Sandbox, a *ActualResourceSnapshot) { a.Main.RunnerProtocolVersion = 99 }},
		{"bad policy", func(_ *domain.Sandbox, a *ActualResourceSnapshot) {
			a.Main.NetworkMode, a.Main.NetworkPeerContainerID = "container", "egress"
			egress := actualEgress(actualTestID, "egress")
			egress.EgressPolicyHash = "invalid"
			a.Egress = &egress
		}},
		{"bad netns", func(_ *domain.Sandbox, a *ActualResourceSnapshot) { a.Main.NetworkMode = "container" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := stored
			observation := cloneActualSnapshot(actual)
			tt.mutate(&record, &observation)
			store := &metadataRepairTestStore{record: record}
			executor := mustMetadataRepairExecutor(t, store, func(string) bool { return true })
			if _, err := executor.Execute(context.Background(), plan, observation); !errors.Is(err, domain.ErrConflict) || len(store.updates) != 0 {
				t.Fatalf("unsafe repair: err=%v updates=%#v", err, store.updates)
			}
		})
	}
}

// TestMetadataRepairBoundsConflictsAndRejectsOtherPlans 验证有限重试且不会执行 import/wake 计划。
func TestMetadataRepairBoundsConflictsAndRejectsOtherPlans(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateCreating)
	stored.RuntimeID = "old"
	actual := recoveryPlanActual()
	store := &metadataRepairTestStore{record: stored, conflicts: metadataRepairCASAttempts}
	executor := mustMetadataRepairExecutor(t, store, func(string) bool { return true })
	if _, err := executor.Execute(context.Background(), PlanRecovery(&stored, &actual), actual); !errors.Is(err, domain.ErrConflict) || len(store.updates) != metadataRepairCASAttempts {
		t.Fatalf("bounded conflict: err=%v updates=%d", err, len(store.updates))
	}
	if _, err := executor.Execute(context.Background(), RecoveryPlan{Action: RecoveryActionImport}, actual); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("import plan executed: %v", err)
	}
}

func mustMetadataRepairExecutor(t *testing.T, store MetadataRepairStore, wake RecoveryWake) *MetadataRepairExecutor {
	t.Helper()
	executor, err := NewMetadataRepairExecutor(store, newManualClock(time.Unix(100, 0).UTC()), wake)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}
