package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

var reliabilityNow = time.Date(2027, 4, 5, 6, 7, 8, 901234567, time.UTC)

// createReliabilitySandbox 按指定状态创建 revision=1 的可靠性端口测试记录。
func createReliabilitySandbox(
	t *testing.T,
	store *Store,
	id string,
	desired domain.DesiredState,
	observed domain.SandboxState,
) domain.Sandbox {
	t.Helper()
	sandbox := createTestSandbox()
	sandbox.ID = id
	sandbox.DesiredState = desired
	sandbox.ObservedState = observed
	expiresAt := reliabilityNow.Add(time.Hour)
	sandbox.ExpiresAt = &expiresAt
	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("create sandbox %q: %v", id, err)
	}
	created, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get sandbox %q: %v", id, err)
	}
	return created
}

// TestRenewCASAndStatePreconditions 验证续期只会延长有效的 Running 租约。
func TestRenewCASAndStatePreconditions(t *testing.T) {
	store := migrateTestStore(t)
	original := createReliabilitySandbox(t, store, "renew-ok", domain.DesiredRunning, domain.StateRunning)
	location := time.FixedZone("UTC-7", -7*60*60)
	newExpiry := reliabilityNow.Add(2 * time.Hour).In(location)

	got, err := store.Renew(context.Background(), storeport.RenewUpdate{
		ID: original.ID, ExpectedRevision: original.Revision, Now: reliabilityNow.In(location), ExpiresAt: newExpiry,
	})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got.Revision != 2 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(newExpiry) ||
		got.NextReconcileAt == nil || !got.NextReconcileAt.Equal(reliabilityNow) {
		t.Fatalf("renew result: %#v", got)
	}

	cases := []struct {
		name     string
		id       string
		desired  domain.DesiredState
		observed domain.SandboxState
		mutate   func(*storeport.RenewUpdate)
	}{
		{"stale revision", "renew-stale", domain.DesiredRunning, domain.StateRunning, func(update *storeport.RenewUpdate) { update.ExpectedRevision = 0 }},
		{"delete intent", "renew-deleting", domain.DesiredTerminated, domain.StateRunning, nil},
		{"observed terminated", "renew-terminated", domain.DesiredRunning, domain.StateTerminated, nil},
		{"shorten lease", "renew-shorter", domain.DesiredRunning, domain.StateRunning, func(update *storeport.RenewUpdate) { update.ExpiresAt = reliabilityNow.Add(30 * time.Minute) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record := createReliabilitySandbox(t, store, testCase.id, testCase.desired, testCase.observed)
			update := storeport.RenewUpdate{ID: record.ID, ExpectedRevision: record.Revision, Now: reliabilityNow, ExpiresAt: reliabilityNow.Add(2 * time.Hour)}
			if testCase.mutate != nil {
				testCase.mutate(&update)
			}
			if _, err := store.Renew(context.Background(), update); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("got %v, want conflict", err)
			}
		})
	}
}

// TestExpireIntentRequiresCurrentDueLease 验证旧 timer、未到期租约和删除态不能提交 expire。
func TestExpireIntentRequiresCurrentDueLease(t *testing.T) {
	store := migrateTestStore(t)
	record := createReliabilitySandbox(t, store, "expire-ok", domain.DesiredRunning, domain.StateFailed)
	due := *record.ExpiresAt
	now := due.Add(time.Second)

	got, err := store.ExpireIntent(context.Background(), storeport.ExpireIntentUpdate{
		ID: record.ID, ExpectedRevision: record.Revision, ExpectedExpiresAt: due, Now: now,
	})
	if err != nil {
		t.Fatalf("expire intent: %v", err)
	}
	if got.DesiredState != domain.DesiredTerminated || got.Revision != 2 ||
		got.NextReconcileAt == nil || !got.NextReconcileAt.Equal(now) {
		t.Fatalf("expire result: %#v", got)
	}

	for _, testCase := range []struct {
		name    string
		id      string
		mutate  func(*storeport.ExpireIntentUpdate)
		desired domain.DesiredState
	}{
		{"stale revision", "expire-stale", func(update *storeport.ExpireIntentUpdate) { update.ExpectedRevision = 0 }, domain.DesiredRunning},
		{"old timer", "expire-old-timer", func(update *storeport.ExpireIntentUpdate) {
			update.ExpectedExpiresAt = update.ExpectedExpiresAt.Add(-time.Second)
		}, domain.DesiredRunning},
		{"already deleting", "expire-deleting", nil, domain.DesiredTerminated},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			item := createReliabilitySandbox(t, store, testCase.id, testCase.desired, domain.StateRunning)
			update := storeport.ExpireIntentUpdate{ID: item.ID, ExpectedRevision: item.Revision, ExpectedExpiresAt: *item.ExpiresAt, Now: item.ExpiresAt.Add(time.Second)}
			if testCase.mutate != nil {
				testCase.mutate(&update)
			}
			if _, err := store.ExpireIntent(context.Background(), update); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("got %v, want conflict", err)
			}
		})
	}
}

// TestScheduleAndResetRetryAreAtomic 验证失败调度与成功清零都携带完整观测结果。
func TestScheduleAndResetRetryAreAtomic(t *testing.T) {
	store := migrateTestStore(t)
	record := createReliabilitySandbox(t, store, "retry", domain.DesiredRunning, domain.StateCreating)
	next := reliabilityNow.Add(17 * time.Second)
	failed, err := store.ScheduleRetry(context.Background(), storeport.RetryUpdate{
		ID: record.ID, ExpectedRevision: record.Revision, AttemptedAt: reliabilityNow,
		NextReconcileAt: next, Reason: "RUNTIME_UNAVAILABLE", Message: "Runtime is temporarily unavailable.",
	})
	if err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if failed.ObservedState != domain.StateFailed || failed.RetryAttempt != 1 ||
		failed.NextReconcileAt == nil || !failed.NextReconcileAt.Equal(next) ||
		failed.LastReconcileAt == nil || !failed.LastReconcileAt.Equal(reliabilityNow) {
		t.Fatalf("scheduled result: %#v", failed)
	}

	resetAt := reliabilityNow.Add(time.Minute)
	reset, err := store.ResetRetry(context.Background(), storeport.RetryResetUpdate{
		Observed: storeport.ObservedUpdate{
			ID: failed.ID, ExpectedRevision: failed.Revision, State: domain.StateRunning,
			Reason: "RUNNING", Message: "Sandbox is running.", RuntimeID: "runtime-2",
		},
		ReconciledAt: resetAt,
	})
	if err != nil {
		t.Fatalf("reset retry: %v", err)
	}
	if reset.ObservedState != domain.StateRunning || reset.RetryAttempt != 0 ||
		reset.NextReconcileAt != nil || reset.LastReconcileAt == nil ||
		!reset.LastReconcileAt.Equal(resetAt) || reset.RuntimeID != "runtime-2" {
		t.Fatalf("reset result: %#v", reset)
	}

	terminated := createReliabilitySandbox(t, store, "retry-terminal", domain.DesiredTerminated, domain.StateTerminated)
	if _, err := store.ScheduleRetry(context.Background(), storeport.RetryUpdate{
		ID: terminated.ID, ExpectedRevision: terminated.Revision, AttemptedAt: reliabilityNow,
		NextReconcileAt: next, Reason: "DELETE_FAILED",
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("terminal schedule: got %v, want conflict", err)
	}
	if _, err := store.ResetRetry(context.Background(), storeport.RetryResetUpdate{
		Observed:     storeport.ObservedUpdate{ID: reset.ID, ExpectedRevision: failed.Revision, State: domain.StateRunning},
		ReconciledAt: resetAt,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale reset: got %v, want conflict", err)
	}
	pending := createReliabilitySandbox(t, store, "reset-intermediate", domain.DesiredRunning, domain.StateCreating)
	if _, err := store.ResetRetry(context.Background(), storeport.RetryResetUpdate{
		Observed:     storeport.ObservedUpdate{ID: pending.ID, ExpectedRevision: pending.Revision, State: domain.StateCreating},
		ReconciledAt: resetAt,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("intermediate reset: got %v, want conflict", err)
	}
}

// TestRecordHealthResultEnforcesRunningState 验证健康结果计数、归零和旧结果隔离。
func TestRecordHealthResultEnforcesRunningState(t *testing.T) {
	store := migrateTestStore(t)
	record := createReliabilitySandbox(t, store, "health", domain.DesiredRunning, domain.StateRunning)
	failed, err := store.RecordHealthResult(context.Background(), storeport.HealthResultUpdate{
		ID: record.ID, ExpectedRevision: record.Revision, CheckedAt: reliabilityNow, Healthy: false,
	})
	if err != nil {
		t.Fatalf("record failed health: %v", err)
	}
	if failed.HealthFailureCount != 1 || failed.LastReconcileAt == nil || !failed.LastReconcileAt.Equal(reliabilityNow) {
		t.Fatalf("failed health result: %#v", failed)
	}
	healthyAt := reliabilityNow.Add(time.Second)
	healthy, err := store.RecordHealthResult(context.Background(), storeport.HealthResultUpdate{
		ID: failed.ID, ExpectedRevision: failed.Revision, CheckedAt: healthyAt, Healthy: true,
	})
	if err != nil {
		t.Fatalf("record healthy result: %v", err)
	}
	if healthy.HealthFailureCount != 0 || healthy.LastReconcileAt == nil || !healthy.LastReconcileAt.Equal(healthyAt) {
		t.Fatalf("healthy result: %#v", healthy)
	}

	deleting := createReliabilitySandbox(t, store, "health-deleting", domain.DesiredTerminated, domain.StateRunning)
	if _, err := store.RecordHealthResult(context.Background(), storeport.HealthResultUpdate{
		ID: deleting.ID, ExpectedRevision: deleting.Revision, CheckedAt: reliabilityNow,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete intent health: got %v, want conflict", err)
	}
}

// TestReliabilityUpdatesNotFoundAndReopen 验证 typed not-found 与时间字段跨 reopen 保持不变。
func TestReliabilityUpdatesNotFoundAndReopen(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := first.Renew(context.Background(), storeport.RenewUpdate{
		ID: "missing", ExpectedRevision: 1, Now: reliabilityNow, ExpiresAt: reliabilityNow.Add(time.Hour),
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing renew: got %v, want not found", err)
	}
	record := createReliabilitySandbox(t, first, "reopen", domain.DesiredRunning, domain.StateRunning)
	checkedAt := reliabilityNow.In(time.FixedZone("UTC+9", 9*60*60))
	updated, err := first.RecordHealthResult(context.Background(), storeport.HealthResultUpdate{
		ID: record.ID, ExpectedRevision: record.Revision, CheckedAt: checkedAt,
	})
	if err != nil {
		t.Fatalf("record before reopen: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	got, err := second.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got.Revision != updated.Revision || got.LastReconcileAt == nil || !got.LastReconcileAt.Equal(checkedAt) {
		t.Fatalf("reopened result: %#v", got)
	}
}
