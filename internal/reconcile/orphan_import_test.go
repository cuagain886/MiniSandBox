package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type orphanImportTestStore struct {
	record   *domain.Sandbox
	imports  []storeport.RecoveredImportRequest
	conflict bool
}

func (s *orphanImportTestStore) ImportRecovered(_ context.Context, request storeport.RecoveredImportRequest) (domain.Sandbox, error) {
	s.imports = append(s.imports, request)
	if s.conflict || s.record != nil {
		return domain.Sandbox{}, domain.ErrConflict
	}
	record := request.Sandbox
	record.Revision = 1
	s.record = &record
	return record, nil
}

func (s *orphanImportTestStore) Get(context.Context, string) (domain.Sandbox, error) {
	if s.record == nil {
		return domain.Sandbox{}, domain.ErrNotFound
	}
	return *s.record, nil
}

// TestOrphanImportExecutorImportsTrustedBundleAndWakes 验证完整可信 orphan 以保守 Creating 原子导入。
func TestOrphanImportExecutorImportsTrustedBundleAndWakes(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	store := &orphanImportTestStore{}
	wakes := 0
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { wakes++; return true }, true)
	result, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil || result.Replayed || wakes != 1 || len(store.imports) != 1 {
		t.Fatalf("import: %#v wakes=%d imports=%#v err=%v", result, wakes, store.imports, err)
	}
	got := result.Sandbox
	if got.Origin != domain.SandboxOriginRecoveredOrphan || got.DesiredState != domain.DesiredRunning || got.ObservedState != domain.StateCreating ||
		got.Reason != domain.SandboxReasonOrphanImported || got.RuntimeID != actual.Main.ContainerID {
		t.Fatalf("record: %#v", got)
	}
}

// TestOrphanImportExecutorReplaysConcurrentWinner 验证唯一冲突后重读同一导入并幂等 Wake。
func TestOrphanImportExecutorReplaysConcurrentWinner(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	store := &orphanImportTestStore{}
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
	first, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil || !second.Replayed || second.Sandbox.ID != first.Sandbox.ID || len(store.imports) != 2 {
		t.Fatalf("replay: %#v/%v imports=%d", second, err, len(store.imports))
	}
}

// TestOrphanImportExecutorRejectsExistingDifferentRecord 验证 ID 已属于 API 或不同 runtime 时不覆盖。
func TestOrphanImportExecutorRejectsExistingDifferentRecord(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	existing := recoveryPlanSandbox(domain.DesiredRunning, domain.StateRunning)
	existing.Origin = domain.SandboxOriginAPI
	store := &orphanImportTestStore{record: &existing}
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
	if _, err := executor.Execute(context.Background(), plan, actual, expected); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("different record accepted: %v", err)
	}
}

// TestOrphanImportExecutorRejectsPartialProfileHashAndUnknownLease 验证不完整、漂移或 v1 无 manifest 都不写 Store。
func TestOrphanImportExecutorRejectsPartialProfileHashAndUnknownLease(t *testing.T) {
	now, baseline, plan, expected := orphanImportFixture(t)
	tests := []struct {
		name   string
		mutate func(*ActualResourceSnapshot)
	}{
		{"partial", func(a *ActualResourceSnapshot) { a.Workspace = nil }},
		{"hash", func(a *ActualResourceSnapshot) { a.Main.SpecHash = "different" }},
		{"profile", func(a *ActualResourceSnapshot) { a.Main.Privileged = true }},
		{"v1 no manifest", func(a *ActualResourceSnapshot) { a.Main.SchemaVersion = 1; a.Directory.Manifest = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := cloneActualSnapshot(baseline)
			tt.mutate(&actual)
			store := &orphanImportTestStore{}
			executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
			if _, err := executor.Execute(context.Background(), plan, actual, expected); err == nil || len(store.imports) != 0 {
				t.Fatalf("unsafe import: err=%v imports=%#v", err, store.imports)
			}
		})
	}
}

// TestOrphanImportExecutorPrefersRenewedManifestAndHonorsDisable 验证 manifest 新租约优先于创建快照且关闭配置不写入。
func TestOrphanImportExecutorPrefersRenewedManifestAndHonorsDisable(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	old := now.Add(-time.Minute)
	actual.Main.CreationExpiresAt = &old
	renewed := now.Add(time.Hour)
	actual.Directory.Manifest.ExpiresAt = renewed
	store := &orphanImportTestStore{}
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
	result, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil || result.Sandbox.ExpiresAt == nil || !result.Sandbox.ExpiresAt.Equal(renewed) {
		t.Fatalf("renewed manifest: %#v/%v", result, err)
	}
	disabledStore := &orphanImportTestStore{}
	disabled := mustOrphanImportExecutor(t, disabledStore, now, func(string) bool { return true }, false)
	if _, err := disabled.Execute(context.Background(), plan, actual, expected); !errors.Is(err, ErrOrphanImportDisabled) || len(disabledStore.imports) != 0 {
		t.Fatalf("disabled import: %v %#v", err, disabledStore.imports)
	}
}

// TestOrphanImportExecutorImportsExpiredManifestIntoDeletePath 验证 manifest 明确过期时导入删除意图而非直接删除。
func TestOrphanImportExecutorImportsExpiredManifestIntoDeletePath(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	for _, expiry := range []time.Time{now.Add(-time.Second), now} {
		candidate := cloneActualSnapshot(actual)
		candidate.Directory.Manifest.ExpiresAt = expiry
		store := &orphanImportTestStore{}
		wakes := 0
		executor := mustOrphanImportExecutor(t, store, now, func(string) bool { wakes++; return true }, true)
		result, err := executor.Execute(context.Background(), plan, candidate, expected)
		if err != nil || wakes != 1 || result.Sandbox.DesiredState != domain.DesiredTerminated ||
			result.Sandbox.ObservedState != domain.StateStopping || result.Sandbox.Reason != domain.SandboxReasonOrphanExpired {
			t.Fatalf("expiry=%v result=%#v wakes=%d err=%v", expiry, result, wakes, err)
		}
	}
}

// TestOrphanImportExecutorUsesV2CreationFallbackButNotV1 验证仅 v2 可在 manifest 缺失时使用创建 expiry。
func TestOrphanImportExecutorUsesV2CreationFallbackButNotV1(t *testing.T) {
	now, actual, _, expected := orphanImportFixture(t)
	actual.Directory.Manifest = nil
	expired := now.Add(-time.Second)
	actual.Main.CreationExpiresAt = &expired
	plan := PlanRecovery(nil, &actual)
	if plan.Action != RecoveryActionImport {
		t.Fatalf("v2 fallback plan: %#v", plan)
	}
	store := &orphanImportTestStore{}
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
	result, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil || result.Sandbox.DesiredState != domain.DesiredTerminated {
		t.Fatalf("v2 fallback: %#v/%v", result, err)
	}

	v1 := cloneActualSnapshot(actual)
	v1.Main.SchemaVersion = 1
	if got := PlanRecovery(nil, &v1); got.Action != RecoveryActionAnomaly {
		t.Fatalf("v1 fallback was trusted: %#v", got)
	}
}

// TestOrphanImportExecutorRepeatsExpiredRecoveryIdempotently 验证过期 orphan 重复恢复只重读同一删除记录。
func TestOrphanImportExecutorRepeatsExpiredRecoveryIdempotently(t *testing.T) {
	now, actual, plan, expected := orphanImportFixture(t)
	actual.Directory.Manifest.ExpiresAt = now
	store := &orphanImportTestStore{}
	executor := mustOrphanImportExecutor(t, store, now, func(string) bool { return true }, true)
	first, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), plan, actual, expected)
	if err != nil || !second.Replayed || first.Sandbox.DesiredState != domain.DesiredTerminated || second.Sandbox.ID != first.Sandbox.ID {
		t.Fatalf("expired replay: first=%#v second=%#v err=%v", first, second, err)
	}
}

func orphanImportFixture(t *testing.T) (time.Time, ActualResourceSnapshot, RecoveryPlan, DriftExpectation) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	_, actual, expected := driftFixture(false)
	actual.Main.CreatedAt = now.Add(-time.Minute)
	expires := now.Add(time.Hour)
	actual.Directory.Manifest.ExpiresAt = expires
	actual.Directory.Manifest.ProjectedStoreRevision = 7
	return now, actual, PlanRecovery(nil, &actual), expected
}

func mustOrphanImportExecutor(t *testing.T, store OrphanImportStore, now time.Time, wake RecoveryWake, enabled bool) *OrphanImportExecutor {
	t.Helper()
	executor, err := NewOrphanImportExecutor(store, newManualClock(now), wake, enabled)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}
