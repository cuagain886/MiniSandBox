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

// desiredUpdateTime 返回 P1-019 测试使用的确定性更新时间。
func desiredUpdateTime() time.Time {
	return time.Date(2027, 2, 3, 4, 5, 6, 789012345, time.UTC)
}

// prepareDesiredUpdateStore 创建 DesiredRunning 初始记录并固定 Store 时钟。
func prepareDesiredUpdateStore(t *testing.T) (*Store, domain.Sandbox) {
	t.Helper()

	store := migrateTestStore(t)
	store.now = desiredUpdateTime
	sandbox := createTestSandbox()
	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	sandbox.Revision = 1
	sandbox.CreatedAt = sandbox.CreatedAt.UTC()
	sandbox.UpdatedAt = sandbox.UpdatedAt.UTC()
	sandbox.LastTransitionAt = sandbox.LastTransitionAt.UTC()
	return store, sandbox
}

// TestUpdateDesiredTerminatedCAS 验证首次提交只更新期望状态、revision 和 updated time。
func TestUpdateDesiredTerminatedCAS(t *testing.T) {
	store, original := prepareDesiredUpdateStore(t)

	got, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredTerminated,
		original.Revision,
	)
	if err != nil {
		t.Fatalf("update desired: %v", err)
	}

	want := original
	want.DesiredState = domain.DesiredTerminated
	want.Revision++
	want.UpdatedAt = desiredUpdateTime()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updated sandbox mismatch:\n got: %#v\nwant: %#v", got, want)
	}
	if got.ObservedState != original.ObservedState {
		t.Fatalf(
			"desired update changed observed state: got %q, want %q",
			got.ObservedState,
			original.ObservedState,
		)
	}
	if !got.LastTransitionAt.Equal(original.LastTransitionAt) {
		t.Fatalf(
			"desired update changed transition time: got %v, want %v",
			got.LastTransitionAt,
			original.LastTransitionAt,
		)
	}

	persisted, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get committed sandbox: %v", err)
	}
	if !reflect.DeepEqual(persisted, want) {
		t.Fatalf("committed sandbox mismatch:\n got: %#v\nwant: %#v", persisted, want)
	}
}

// TestUpdateDesiredTerminatedRetryIsNoOp 验证响应丢失后的旧 revision 重试仍幂等成功。
func TestUpdateDesiredTerminatedRetryIsNoOp(t *testing.T) {
	store, original := prepareDesiredUpdateStore(t)
	first, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredTerminated,
		original.Revision,
	)
	if err != nil {
		t.Fatalf("first desired update: %v", err)
	}

	later := desiredUpdateTime().Add(time.Hour)
	store.now = func() time.Time { return later }
	retried, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredTerminated,
		original.Revision,
	)
	if err != nil {
		t.Fatalf("retry desired update: %v", err)
	}
	if !reflect.DeepEqual(retried, first) {
		t.Fatalf("retry was not a no-op:\n got: %#v\nwant: %#v", retried, first)
	}
}

// TestUpdateDesiredConflict 验证仍为 Running 时旧 revision 返回冲突且记录不变。
func TestUpdateDesiredConflict(t *testing.T) {
	store, original := prepareDesiredUpdateStore(t)

	got, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredTerminated,
		original.Revision+1,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("update stale revision: got %v, want ErrConflict", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("conflict returned partial value: %#v", got)
	}

	persisted, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get after conflict: %v", err)
	}
	if !reflect.DeepEqual(persisted, original) {
		t.Fatalf("conflict changed record:\n got: %#v\nwant: %#v", persisted, original)
	}
}

// TestUpdateDesiredNotFound 验证不存在的 ID 统一返回 domain.ErrNotFound。
func TestUpdateDesiredNotFound(t *testing.T) {
	store, _ := prepareDesiredUpdateStore(t)

	got, err := store.UpdateDesired(
		context.Background(),
		"missing",
		domain.DesiredTerminated,
		1,
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing sandbox: got %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("not found returned partial value: %#v", got)
	}
}

// TestUpdateDesiredRollsBackWhenReadbackFails 验证事务内回读损坏记录会撤销期望状态更新。
func TestUpdateDesiredRollsBackWhenReadbackFails(t *testing.T) {
	store, original := prepareDesiredUpdateStore(t)
	if _, err := store.db.Exec(
		"UPDATE sandboxes SET spec_json = ? WHERE id = ?",
		[]byte(`{}`),
		original.ID,
	); err != nil {
		t.Fatalf("corrupt stored spec: %v", err)
	}

	_, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredTerminated,
		original.Revision,
	)
	if !errors.Is(err, storeport.ErrCorrupt) {
		t.Fatalf("update corrupt row: got %v, want ErrCorrupt", err)
	}

	var (
		desiredState  string
		observedState string
		revision      uint64
		updatedAt     string
		transitionAt  string
	)
	if err := store.db.QueryRow(
		`SELECT
			desired_state,
			observed_state,
			revision,
			updated_at,
			last_transition_at
		FROM sandboxes
		WHERE id = ?`,
		original.ID,
	).Scan(
		&desiredState,
		&observedState,
		&revision,
		&updatedAt,
		&transitionAt,
	); err != nil {
		t.Fatalf("read row after rollback: %v", err)
	}
	if desiredState != string(original.DesiredState) ||
		observedState != string(original.ObservedState) ||
		revision != original.Revision ||
		updatedAt != original.UpdatedAt.Format(time.RFC3339Nano) ||
		transitionAt != original.LastTransitionAt.Format(time.RFC3339Nano) {
		t.Fatalf(
			"failed readback was not rolled back: desired=%q observed=%q revision=%d updated=%q transition=%q",
			desiredState,
			observedState,
			revision,
			updatedAt,
			transitionAt,
		)
	}
}

// TestUpdateDesiredRejectsUnsupportedTarget 验证 Phase 1 不允许通过该方法复活 sandbox。
func TestUpdateDesiredRejectsUnsupportedTarget(t *testing.T) {
	store, original := prepareDesiredUpdateStore(t)

	_, err := store.UpdateDesired(
		context.Background(),
		original.ID,
		domain.DesiredRunning,
		original.Revision,
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("update unsupported desired state: got %v, want ErrInvalid", err)
	}

	persisted, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get after invalid update: %v", err)
	}
	if !reflect.DeepEqual(persisted, original) {
		t.Fatalf("invalid update changed record: %#v", persisted)
	}
}
