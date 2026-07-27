package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// candidateFixture 描述候选矩阵中的一条持久化记录。
type candidateFixture struct {
	id       string
	desired  domain.DesiredState
	observed domain.SandboxState
	reason   string
	minute   int
}

// populateCandidateMatrix 插入覆盖全部 Phase 1 候选分支的乱序记录。
func populateCandidateMatrix(t *testing.T) *Store {
	t.Helper()

	store := migrateTestStore(t)
	base := time.Date(2027, 3, 4, 5, 0, 0, 0, time.UTC)
	fixtures := []candidateFixture{
		{
			id:       "stable-running",
			desired:  domain.DesiredRunning,
			observed: domain.StateRunning,
			reason:   "RUNNING",
			minute:   9,
		},
		{
			id:       "delete-running",
			desired:  domain.DesiredTerminated,
			observed: domain.StateRunning,
			reason:   "RUNNING",
			minute:   7,
		},
		{
			id:       "cleanup-b",
			desired:  domain.DesiredRunning,
			observed: domain.StateFailed,
			reason:   "CLEANUP_PENDING",
			minute:   2,
		},
		{
			id:       "ordinary-failure",
			desired:  domain.DesiredRunning,
			observed: domain.StateFailed,
			reason:   "IMAGE_PULL_FAILED",
			minute:   1,
		},
		{
			id:       "pending",
			desired:  domain.DesiredRunning,
			observed: domain.StatePending,
			reason:   "CREATE_ACCEPTED",
			minute:   5,
		},
		{
			id:       "stable-terminated",
			desired:  domain.DesiredTerminated,
			observed: domain.StateTerminated,
			reason:   "TERMINATED",
			minute:   0,
		},
		{
			id:       "running-but-terminated",
			desired:  domain.DesiredRunning,
			observed: domain.StateTerminated,
			reason:   "TERMINATED",
			minute:   8,
		},
		{
			id:       "creating",
			desired:  domain.DesiredRunning,
			observed: domain.StateCreating,
			reason:   "CREATING_RUNTIME",
			minute:   3,
		},
		{
			id:       "delete-failed",
			desired:  domain.DesiredTerminated,
			observed: domain.StateFailed,
			reason:   "IMAGE_PULL_FAILED",
			minute:   6,
		},
		{
			id:       "cleanup-a",
			desired:  domain.DesiredRunning,
			observed: domain.StateFailed,
			reason:   "CLEANUP_PENDING",
			minute:   2,
		},
		{
			id:       "stopping",
			desired:  domain.DesiredTerminated,
			observed: domain.StateStopping,
			reason:   "DELETING_RUNTIME",
			minute:   4,
		},
	}

	for _, fixture := range fixtures {
		sandbox := createTestSandbox()
		sandbox.ID = fixture.id
		sandbox.DesiredState = fixture.desired
		sandbox.ObservedState = fixture.observed
		sandbox.Reason = fixture.reason
		sandbox.CreatedAt = base.Add(time.Duration(fixture.minute) * time.Minute)
		sandbox.UpdatedAt = sandbox.CreatedAt
		sandbox.LastTransitionAt = sandbox.CreatedAt
		if err := store.Create(context.Background(), sandbox); err != nil {
			t.Fatalf("create fixture %q: %v", fixture.id, err)
		}
	}
	return store
}

// TestListReconcileCandidatesStateMatrix 验证候选状态组合和稳定排序。
func TestListReconcileCandidatesStateMatrix(t *testing.T) {
	store := populateCandidateMatrix(t)

	got, err := store.ListReconcileCandidates(context.Background(), 100)
	if err != nil {
		t.Fatalf("list reconcile candidates: %v", err)
	}

	wantIDs := []string{
		"cleanup-a",
		"cleanup-b",
		"creating",
		"stopping",
		"pending",
		"delete-failed",
		"delete-running",
		"running-but-terminated",
	}
	gotIDs := make([]string, 0, len(got))
	for _, sandbox := range got {
		gotIDs = append(gotIDs, sandbox.ID)
		if sandbox.Revision != 1 {
			t.Fatalf("candidate %q revision: got %d, want 1", sandbox.ID, sandbox.Revision)
		}
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("candidate IDs:\n got: %v\nwant: %v", gotIDs, wantIDs)
	}
}

// TestListReconcileCandidatesLimit 验证 limit 截断稳定排序后的结果。
func TestListReconcileCandidatesLimit(t *testing.T) {
	store := populateCandidateMatrix(t)

	got, err := store.ListReconcileCandidates(context.Background(), 3)
	if err != nil {
		t.Fatalf("list limited candidates: %v", err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, sandbox := range got {
		gotIDs = append(gotIDs, sandbox.ID)
	}
	wantIDs := []string{"cleanup-a", "cleanup-b", "creating"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("limited IDs: got %v, want %v", gotIDs, wantIDs)
	}
}

// TestListReconcileCandidatesRejectsInvalidLimit 验证非正 limit 不执行无界查询。
func TestListReconcileCandidatesRejectsInvalidLimit(t *testing.T) {
	store := migrateTestStore(t)

	for _, limit := range []int{0, -1} {
		got, err := store.ListReconcileCandidates(context.Background(), limit)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("limit %d: got %v, want ErrInvalid", limit, err)
		}
		if got != nil {
			t.Fatalf("limit %d returned candidates: %#v", limit, got)
		}
	}
}

// TestListReconcileCandidatesEmpty 验证没有候选时返回可直接迭代的空切片。
func TestListReconcileCandidatesEmpty(t *testing.T) {
	store := migrateTestStore(t)
	stable := createTestSandbox()
	stable.ObservedState = domain.StateRunning
	stable.Reason = "RUNNING"
	if err := store.Create(context.Background(), stable); err != nil {
		t.Fatalf("create stable sandbox: %v", err)
	}

	got, err := store.ListReconcileCandidates(context.Background(), 10)
	if err != nil {
		t.Fatalf("list empty candidates: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty candidates: got %#v, want non-nil empty slice", got)
	}
}
