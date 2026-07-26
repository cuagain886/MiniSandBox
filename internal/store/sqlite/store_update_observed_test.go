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

// observedUpdateTime 返回 P1-018 测试使用的确定性更新时间。
func observedUpdateTime() time.Time {
	return time.Date(2027, 1, 2, 3, 4, 5, 678901234, time.UTC)
}

// prepareObservedUpdateStore 创建初始记录并固定 Store 时钟。
func prepareObservedUpdateStore(t *testing.T) (*Store, domain.Sandbox) {
	t.Helper()

	store := migrateTestStore(t)
	store.now = observedUpdateTime
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

// TestUpdateObservedCAS 验证成功 CAS 更新字段、revision 和状态转换时间。
func TestUpdateObservedCAS(t *testing.T) {
	store, original := prepareObservedUpdateStore(t)
	update := storeport.ObservedUpdate{
		ID:               original.ID,
		ExpectedRevision: original.Revision,
		State:            domain.StateCreating,
		Reason:           "CREATING_RUNTIME",
		Message:          "Sandbox runtime is being created.",
		RuntimeID:        "runtime-01",
	}

	got, err := store.UpdateObserved(context.Background(), update)
	if err != nil {
		t.Fatalf("update observed: %v", err)
	}

	want := original
	want.ObservedState = update.State
	want.Reason = update.Reason
	want.Message = update.Message
	want.RuntimeID = update.RuntimeID
	want.Revision = original.Revision + 1
	want.UpdatedAt = observedUpdateTime()
	want.LastTransitionAt = observedUpdateTime()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updated sandbox mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	persisted, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get committed sandbox: %v", err)
	}
	if !reflect.DeepEqual(persisted, want) {
		t.Fatalf("committed sandbox mismatch:\n got: %#v\nwant: %#v", persisted, want)
	}
}

// TestUpdateObservedSameStatePreservesTransitionTime 验证同状态更新不制造虚假状态转换。
func TestUpdateObservedSameStatePreservesTransitionTime(t *testing.T) {
	store, original := prepareObservedUpdateStore(t)
	update := storeport.ObservedUpdate{
		ID:               original.ID,
		ExpectedRevision: original.Revision,
		State:            original.ObservedState,
		Reason:           "CREATE_RECHECKED",
		Message:          "Sandbox creation is still pending.",
	}

	got, err := store.UpdateObserved(context.Background(), update)
	if err != nil {
		t.Fatalf("update same observed state: %v", err)
	}
	if got.Revision != original.Revision+1 {
		t.Fatalf("revision: got %d, want %d", got.Revision, original.Revision+1)
	}
	if !got.UpdatedAt.Equal(observedUpdateTime()) {
		t.Fatalf("updated_at: got %v, want %v", got.UpdatedAt, observedUpdateTime())
	}
	if !got.LastTransitionAt.Equal(original.LastTransitionAt) {
		t.Fatalf(
			"last_transition_at changed: got %v, want %v",
			got.LastTransitionAt,
			original.LastTransitionAt,
		)
	}
	if got.Reason != update.Reason || got.Message != update.Message {
		t.Fatalf("same-state metadata not updated: %#v", got)
	}
}

// TestUpdateObservedCASConflict 验证所有零受影响行都映射为冲突且不修改记录。
func TestUpdateObservedCASConflict(t *testing.T) {
	tests := []struct {
		name             string
		id               string
		expectedRevision uint64
	}{
		{
			name:             "stale revision",
			id:               "sb-create-01",
			expectedRevision: 99,
		},
		{
			name:             "missing sandbox",
			id:               "missing",
			expectedRevision: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, original := prepareObservedUpdateStore(t)
			got, err := store.UpdateObserved(
				context.Background(),
				storeport.ObservedUpdate{
					ID:               tt.id,
					ExpectedRevision: tt.expectedRevision,
					State:            domain.StateCreating,
					Reason:           "CREATING_RUNTIME",
				},
			)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("update conflict: got %v, want ErrConflict", err)
			}
			if !reflect.DeepEqual(got, domain.Sandbox{}) {
				t.Fatalf("conflict returned partial value: %#v", got)
			}

			persisted, err := store.Get(context.Background(), original.ID)
			if err != nil {
				t.Fatalf("get after conflict: %v", err)
			}
			if !reflect.DeepEqual(persisted, original) {
				t.Fatalf(
					"conflict changed record:\n got: %#v\nwant: %#v",
					persisted,
					original,
				)
			}
		})
	}
}

// TestUpdateObservedRollsBackWhenReadbackFails 验证事务内回读失败会撤销已执行的 UPDATE。
func TestUpdateObservedRollsBackWhenReadbackFails(t *testing.T) {
	store, original := prepareObservedUpdateStore(t)
	if _, err := store.db.Exec(
		"UPDATE sandboxes SET spec_json = ? WHERE id = ?",
		[]byte(`{}`),
		original.ID,
	); err != nil {
		t.Fatalf("corrupt stored spec: %v", err)
	}

	_, err := store.UpdateObserved(
		context.Background(),
		storeport.ObservedUpdate{
			ID:               original.ID,
			ExpectedRevision: original.Revision,
			State:            domain.StateCreating,
			Reason:           "CREATING_RUNTIME",
			Message:          "Sandbox runtime is being created.",
			RuntimeID:        "runtime-01",
		},
	)
	if !errors.Is(err, storeport.ErrCorrupt) {
		t.Fatalf("update corrupt row: got %v, want ErrCorrupt", err)
	}

	var (
		state            string
		reason           string
		message          string
		runtimeID        string
		revision         uint64
		updatedAt        string
		lastTransitionAt string
	)
	if err := store.db.QueryRow(
		`SELECT
			observed_state,
			reason,
			message,
			runtime_id,
			revision,
			updated_at,
			last_transition_at
		FROM sandboxes
		WHERE id = ?`,
		original.ID,
	).Scan(
		&state,
		&reason,
		&message,
		&runtimeID,
		&revision,
		&updatedAt,
		&lastTransitionAt,
	); err != nil {
		t.Fatalf("read row after rollback: %v", err)
	}
	if state != string(original.ObservedState) ||
		reason != original.Reason ||
		message != original.Message ||
		runtimeID != original.RuntimeID ||
		revision != original.Revision ||
		updatedAt != original.UpdatedAt.Format(time.RFC3339Nano) ||
		lastTransitionAt != original.LastTransitionAt.Format(time.RFC3339Nano) {
		t.Fatalf(
			"failed readback was not rolled back: state=%q reason=%q message=%q runtime=%q revision=%d updated=%q transition=%q",
			state,
			reason,
			message,
			runtimeID,
			revision,
			updatedAt,
			lastTransitionAt,
		)
	}
}

// TestUpdateObservedRejectsUnknownState 验证 adapter 不会把未知状态写成持久化损坏。
func TestUpdateObservedRejectsUnknownState(t *testing.T) {
	store, original := prepareObservedUpdateStore(t)

	_, err := store.UpdateObserved(
		context.Background(),
		storeport.ObservedUpdate{
			ID:               original.ID,
			ExpectedRevision: original.Revision,
			State:            domain.SandboxState("Unknown"),
		},
	)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("update unknown state: got %v, want ErrInvalid", err)
	}

	persisted, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get after invalid update: %v", err)
	}
	if !reflect.DeepEqual(persisted, original) {
		t.Fatalf("invalid update changed record: %#v", persisted)
	}
}
