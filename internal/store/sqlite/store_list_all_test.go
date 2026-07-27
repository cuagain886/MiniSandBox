package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// normalizedCreatedSandbox 返回 Create 实际持久化后的初始领域对象。
func normalizedCreatedSandbox(sandbox domain.Sandbox) domain.Sandbox {
	sandbox.Revision = 1
	sandbox.CreatedAt = sandbox.CreatedAt.UTC()
	sandbox.UpdatedAt = sandbox.UpdatedAt.UTC()
	sandbox.LastTransitionAt = sandbox.LastTransitionAt.UTC()
	sandbox.ExpiresAt = nil
	return sandbox
}

// TestListAllAfterReopen 验证关闭重开后全部生命周期和恢复字段无损。
func TestListAllAfterReopen(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate first store: %v", err)
	}

	base := time.Date(2027, 4, 5, 6, 7, 8, 901234567, time.UTC)
	a := createTestSandbox()
	a.ID = "sandbox-a"
	a.CreatedAt = base
	a.UpdatedAt = base
	a.LastTransitionAt = base
	b := createTestSandbox()
	b.ID = "sandbox-b"
	b.CreatedAt = base
	b.UpdatedAt = base
	b.LastTransitionAt = base
	z := createTestSandbox()
	z.ID = "sandbox-z"
	z.CreatedAt = base.Add(time.Minute)
	z.UpdatedAt = z.CreatedAt
	z.LastTransitionAt = z.CreatedAt

	// 故意乱序插入，ListAll 必须按 created_at、id 排序。
	for _, sandbox := range []domain.Sandbox{z, b, a} {
		if err := first.Create(context.Background(), sandbox); err != nil {
			t.Fatalf("create %q: %v", sandbox.ID, err)
		}
	}

	observedAt := base.Add(2 * time.Minute)
	first.now = func() time.Time { return observedAt }
	wantA, err := first.UpdateObserved(
		context.Background(),
		storeport.ObservedUpdate{
			ID:               a.ID,
			ExpectedRevision: 1,
			State:            domain.StateCreating,
			Reason:           "WAITING_RUNNER",
			Message:          "Sandbox runner is starting.",
			RuntimeID:        "runtime-a",
		},
	)
	if err != nil {
		t.Fatalf("update observed a: %v", err)
	}

	desiredAt := base.Add(3 * time.Minute)
	first.now = func() time.Time { return desiredAt }
	wantB, err := first.UpdateDesired(
		context.Background(),
		b.ID,
		domain.DesiredTerminated,
		1,
	)
	if err != nil {
		t.Fatalf("update desired b: %v", err)
	}
	wantZ := normalizedCreatedSandbox(z)

	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer second.Close()
	// 启动流程会重复执行 migration；这里同时验证重开后迁移幂等。
	if err := second.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate reopened store: %v", err)
	}

	got, err := second.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list all after reopen: %v", err)
	}
	want := []domain.Sandbox{wantA, wantB, wantZ}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened records mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestListAllEmpty 验证空数据库返回可直接迭代的非 nil 空切片。
func TestListAllEmpty(t *testing.T) {
	store := migrateTestStore(t)

	got, err := store.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list empty database: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty list: got %#v, want non-nil empty slice", got)
	}
}
