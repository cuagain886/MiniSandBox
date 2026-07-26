package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestGetSandboxRoundTrip 验证 Create 后可以完整还原领域 Sandbox。
func TestGetSandboxRoundTrip(t *testing.T) {
	store := migrateTestStore(t)
	input := createTestSandbox()
	if err := store.Create(context.Background(), input); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	got, err := store.Get(context.Background(), input.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}

	want := input
	want.Revision = 1
	want.CreatedAt = want.CreatedAt.UTC()
	want.UpdatedAt = want.UpdatedAt.UTC()
	want.LastTransitionAt = want.LastTransitionAt.UTC()
	want.ExpiresAt = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestGetSandboxNotFound 验证不存在的 ID 统一映射为 domain.ErrNotFound。
func TestGetSandboxNotFound(t *testing.T) {
	store := migrateTestStore(t)

	got, err := store.Get(context.Background(), "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get missing sandbox: got %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("missing sandbox returned partial value: %#v", got)
	}
}

// TestGetSandboxCorruptRow 验证不可信持久化字段统一返回可分类的损坏错误。
func TestGetSandboxCorruptRow(t *testing.T) {
	const poison = "must-not-leak-secret-value"
	tests := []struct {
		name      string
		statement string
		value     any
	}{
		{
			name:      "unknown desired state",
			statement: "UPDATE sandboxes SET desired_state = ? WHERE id = ?",
			value:     poison,
		},
		{
			name:      "unknown observed state",
			statement: "UPDATE sandboxes SET observed_state = ? WHERE id = ?",
			value:     poison,
		},
		{
			name:      "malformed spec json",
			statement: "UPDATE sandboxes SET spec_json = ? WHERE id = ?",
			value:     []byte(`{"image":` + poison),
		},
		{
			name:      "incomplete spec json",
			statement: "UPDATE sandboxes SET spec_json = ? WHERE id = ?",
			value:     []byte(`{}`),
		},
		{
			name:      "invalid revision",
			statement: "UPDATE sandboxes SET revision = ? WHERE id = ?",
			value:     int64(0),
		},
		{
			name:      "invalid created time",
			statement: "UPDATE sandboxes SET created_at = ? WHERE id = ?",
			value:     poison,
		},
		{
			name:      "invalid updated time",
			statement: "UPDATE sandboxes SET updated_at = ? WHERE id = ?",
			value:     poison,
		},
		{
			name:      "invalid transition time",
			statement: "UPDATE sandboxes SET last_transition_at = ? WHERE id = ?",
			value:     poison,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := migrateTestStore(t)
			sandbox := createTestSandbox()
			if err := store.Create(context.Background(), sandbox); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}
			if _, err := store.db.Exec(tt.statement, tt.value, sandbox.ID); err != nil {
				t.Fatalf("corrupt stored row: %v", err)
			}

			got, err := store.Get(context.Background(), sandbox.ID)
			if !errors.Is(err, storeport.ErrCorrupt) {
				t.Fatalf("get corrupt sandbox: got %v, want ErrCorrupt", err)
			}
			if strings.Contains(err.Error(), poison) {
				t.Fatalf("corruption error leaked stored value: %v", err)
			}
			if !reflect.DeepEqual(got, domain.Sandbox{}) {
				t.Fatalf("corrupt row returned partial value: %#v", got)
			}
		})
	}
}
