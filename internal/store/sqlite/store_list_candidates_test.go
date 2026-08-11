package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

var candidateNow = time.Date(2027, 3, 4, 5, 0, 0, 0, time.UTC)

// createCandidate 插入一条默认未过期且没有调度元数据的记录。
func createCandidate(
	t *testing.T,
	store *Store,
	id string,
	desired domain.DesiredState,
	observed domain.SandboxState,
	reason string,
) {
	t.Helper()
	sandbox := createTestSandbox()
	sandbox.ID = id
	sandbox.DesiredState = desired
	sandbox.ObservedState = observed
	sandbox.Reason = reason
	expiresAt := candidateNow.Add(time.Hour)
	sandbox.ExpiresAt = &expiresAt
	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("create candidate %q: %v", id, err)
	}
}

// setCandidateTimes 只在 adapter 测试中构造持久化调度边界。
func setCandidateTimes(
	t *testing.T,
	store *Store,
	id string,
	expiresAt time.Time,
	nextReconcileAt *time.Time,
	lastReconcileAt *time.Time,
) {
	t.Helper()
	var next any
	if nextReconcileAt != nil {
		next = nextReconcileAt.UTC().Format(time.RFC3339Nano)
	}
	var last any
	if lastReconcileAt != nil {
		last = lastReconcileAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := store.db.Exec(
		`UPDATE sandboxes
		SET expires_at = ?, next_reconcile_at = ?, last_reconcile_at = ?
		WHERE id = ?`,
		expiresAt.UTC().Format(time.RFC3339Nano),
		next,
		last,
		id,
	); err != nil {
		t.Fatalf("set candidate times %q: %v", id, err)
	}
}

// dueQuery 返回固定边界，避免测试依赖墙上时钟。
func dueQuery(afterID string, limit int) storeport.ReconcileCandidateQuery {
	return storeport.ReconcileCandidateQuery{
		Now:           candidateNow,
		RunningCutoff: candidateNow.Add(-30 * time.Second),
		AfterID:       afterID,
		Limit:         limit,
	}
}

// TestListReconcileCandidatesDueMatrix 验证 expiry、retry、cleanup 和健康检查边界。
func TestListReconcileCandidatesDueMatrix(t *testing.T) {
	store := migrateTestStore(t)
	past := candidateNow.Add(-time.Minute)
	future := candidateNow.Add(time.Minute)
	oldHealth := candidateNow.Add(-time.Minute)
	newHealth := candidateNow

	fixtures := []struct {
		id       string
		desired  domain.DesiredState
		observed domain.SandboxState
		reason   string
		next     *time.Time
		last     *time.Time
		expires  time.Time
	}{
		{"cleanup-due", domain.DesiredRunning, domain.StateFailed, cleanupPendingReason, &past, nil, future},
		{"cleanup-future", domain.DesiredRunning, domain.StateFailed, cleanupPendingReason, &future, nil, future},
		{"creating-due", domain.DesiredRunning, domain.StateCreating, "CREATING_RUNTIME", &past, nil, future},
		{"creating-future", domain.DesiredRunning, domain.StateCreating, "CREATING_RUNTIME", &future, nil, future},
		{"delete-failed-due", domain.DesiredTerminated, domain.StateFailed, "DELETE_FAILED", nil, nil, future},
		{"delete-running-due", domain.DesiredTerminated, domain.StateRunning, "RUNNING", nil, nil, future},
		{"expired-bypasses-backoff", domain.DesiredRunning, domain.StateRunning, "RUNNING", &future, &newHealth, past},
		{"ordinary-failed", domain.DesiredRunning, domain.StateFailed, "IMAGE_PULL_FAILED", nil, nil, future},
		{"pending-due", domain.DesiredRunning, domain.StatePending, "CREATE_ACCEPTED", nil, nil, future},
		{"retry-due", domain.DesiredRunning, domain.StateFailed, "RUNTIME_UNAVAILABLE", &past, nil, future},
		{"retry-future", domain.DesiredRunning, domain.StateFailed, "RUNTIME_UNAVAILABLE", &future, nil, future},
		{"running-health-due", domain.DesiredRunning, domain.StateRunning, "RUNNING", nil, &oldHealth, future},
		{"running-health-future", domain.DesiredRunning, domain.StateRunning, "RUNNING", nil, &newHealth, future},
		{"running-health-never", domain.DesiredRunning, domain.StateRunning, "RUNNING", nil, nil, future},
		{"running-terminated-due", domain.DesiredRunning, domain.StateTerminated, "TERMINATED", nil, nil, future},
		{"stable-terminated", domain.DesiredTerminated, domain.StateTerminated, "TERMINATED", nil, nil, future},
		{"stopping-due", domain.DesiredTerminated, domain.StateStopping, "DELETING_RUNTIME", nil, nil, future},
	}
	for _, fixture := range fixtures {
		createCandidate(t, store, fixture.id, fixture.desired, fixture.observed, fixture.reason)
		setCandidateTimes(t, store, fixture.id, fixture.expires, fixture.next, fixture.last)
	}

	got, err := store.ListReconcileCandidates(context.Background(), dueQuery("", 100))
	if err != nil {
		t.Fatalf("list reconcile candidates: %v", err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, sandbox := range got {
		gotIDs = append(gotIDs, sandbox.ID)
	}
	wantIDs := []string{
		"cleanup-due",
		"creating-due",
		"delete-failed-due",
		"delete-running-due",
		"expired-bypasses-backoff",
		"pending-due",
		"retry-due",
		"running-health-due",
		"running-health-never",
		"running-terminated-due",
		"stopping-due",
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("candidate IDs:\n got: %v\nwant: %v", gotIDs, wantIDs)
	}
	if got[0].NextReconcileAt == nil || !got[0].NextReconcileAt.Equal(past) {
		t.Fatalf("candidate metadata was not scanned: %#v", got[0])
	}
}

// TestListReconcileCandidatesKeysetPagination 验证页面间插入和终态变化不会破坏游标语义。
func TestListReconcileCandidatesKeysetPagination(t *testing.T) {
	store := migrateTestStore(t)
	for _, id := range []string{"a-due", "c-due", "e-due"} {
		createCandidate(t, store, id, domain.DesiredRunning, domain.StatePending, "CREATE_ACCEPTED")
	}

	first, err := store.ListReconcileCandidates(context.Background(), dueQuery("", 2))
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if got := []string{first[0].ID, first[1].ID}; !reflect.DeepEqual(got, []string{"a-due", "c-due"}) {
		t.Fatalf("first page IDs: %v", got)
	}

	// b 位于已消费游标之前，不能倒灌到下一页；d 位于游标之后，应被观察到。
	createCandidate(t, store, "b-inserted", domain.DesiredRunning, domain.StatePending, "CREATE_ACCEPTED")
	createCandidate(t, store, "d-inserted", domain.DesiredRunning, domain.StatePending, "CREATE_ACCEPTED")
	if _, err := store.db.Exec(
		`UPDATE sandboxes SET desired_state = ?, observed_state = ? WHERE id = ?`,
		domain.DesiredTerminated,
		domain.StateTerminated,
		"e-due",
	); err != nil {
		t.Fatalf("terminate later candidate: %v", err)
	}

	second, err := store.ListReconcileCandidates(context.Background(), dueQuery("c-due", 2))
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if got := candidateIDs(second); !reflect.DeepEqual(got, []string{"d-inserted"}) {
		t.Fatalf("second page IDs: %v", got)
	}
	restarted, err := store.ListReconcileCandidates(context.Background(), dueQuery("", 10))
	if err != nil {
		t.Fatalf("restart scan: %v", err)
	}
	if got := candidateIDs(restarted); !reflect.DeepEqual(got, []string{"a-due", "b-inserted", "c-due", "d-inserted"}) {
		t.Fatalf("restarted scan IDs: %v", got)
	}
}

// candidateIDs 提取稳定排序断言所需的 sandbox ID。
func candidateIDs(candidates []domain.Sandbox) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}

// TestListReconcileCandidatesRejectsInvalidQuery 验证缺少时间边界或非正 limit 会被拒绝。
func TestListReconcileCandidatesRejectsInvalidQuery(t *testing.T) {
	store := migrateTestStore(t)
	queries := []storeport.ReconcileCandidateQuery{
		{RunningCutoff: candidateNow, Limit: 1},
		{Now: candidateNow, Limit: 1},
		{Now: candidateNow, RunningCutoff: candidateNow, Limit: 0},
		{Now: candidateNow, RunningCutoff: candidateNow, Limit: -1},
	}
	for _, query := range queries {
		got, err := store.ListReconcileCandidates(context.Background(), query)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("query %#v: got %v, want ErrInvalid", query, err)
		}
		if got != nil {
			t.Fatalf("query %#v returned candidates: %#v", query, got)
		}
	}
}

// TestListReconcileCandidatesEmpty 验证没有 due 记录时返回非 nil 空切片。
func TestListReconcileCandidatesEmpty(t *testing.T) {
	store := migrateTestStore(t)
	createCandidate(t, store, "stable", domain.DesiredTerminated, domain.StateTerminated, "TERMINATED")

	got, err := store.ListReconcileCandidates(context.Background(), dueQuery("", 10))
	if err != nil {
		t.Fatalf("list empty candidates: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty candidates: got %#v, want non-nil empty slice", got)
	}
}
