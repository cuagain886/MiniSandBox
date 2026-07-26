package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// createTestSandbox 返回包含完整 Phase 1 持久化字段的 sandbox。
func createTestSandbox() domain.Sandbox {
	location := time.FixedZone("UTC+8", 8*60*60)
	createdAt := time.Date(2026, 7, 26, 19, 30, 1, 123456789, location)

	spec := domain.SandboxSpec{
		Image: "busybox:1.36",
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: domain.WorkspaceSpec{
			MountPath: domain.WorkspaceMountPath,
		},
		Platform: domain.Platform{
			OS:   "linux",
			Arch: "amd64",
		},
	}
	return domain.Sandbox{
		ID:               "sb-create-01",
		Spec:             spec,
		DesiredState:     domain.DesiredRunning,
		ObservedState:    domain.StatePending,
		Reason:           "CREATE_ACCEPTED",
		Message:          "Sandbox creation has been accepted.",
		RuntimeID:        "",
		SpecHash:         spec.Hash(),
		Revision:         99,
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt.Add(time.Second),
		LastTransitionAt: createdAt.Add(2 * time.Second),
	}
}

// migrateTestStore 打开临时数据库、执行 migration 并注册清理。
func migrateTestStore(t *testing.T) *Store {
	t.Helper()

	store := openTestStore(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return store
}

// TestCreateSandbox 验证 Create 原子保存完整初始状态、稳定 spec JSON 和 UTC 时间。
func TestCreateSandbox(t *testing.T) {
	store := migrateTestStore(t)
	sandbox := createTestSandbox()

	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	var (
		specJSON         []byte
		desiredState     string
		observedState    string
		reason           string
		message          string
		runtimeID        string
		specHash         string
		revision         uint64
		createdAt        string
		updatedAt        string
		lastTransitionAt string
	)
	err := store.db.QueryRow(
		`SELECT
			spec_json,
			desired_state,
			observed_state,
			reason,
			message,
			runtime_id,
			spec_hash,
			revision,
			created_at,
			updated_at,
			last_transition_at
		FROM sandboxes
		WHERE id = ?`,
		sandbox.ID,
	).Scan(
		&specJSON,
		&desiredState,
		&observedState,
		&reason,
		&message,
		&runtimeID,
		&specHash,
		&revision,
		&createdAt,
		&updatedAt,
		&lastTransitionAt,
	)
	if err != nil {
		t.Fatalf("read inserted sandbox: %v", err)
	}

	const wantSpecJSON = `{"image":"busybox:1.36","resources":{"cpu_quota_millis":500,"memory_mib":256,"pids":64},"workspace":{"mount_path":"/workspace","persistent":false},"network":{"outbound":false},"platform":{"os":"linux","arch":"amd64"}}`
	if got := string(specJSON); got != wantSpecJSON {
		t.Fatalf("spec_json:\n got: %s\nwant: %s", got, wantSpecJSON)
	}
	if desiredState != string(domain.DesiredRunning) {
		t.Fatalf("desired_state: got %q, want %q", desiredState, domain.DesiredRunning)
	}
	if observedState != string(domain.StatePending) {
		t.Fatalf("observed_state: got %q, want %q", observedState, domain.StatePending)
	}
	if reason != sandbox.Reason || message != sandbox.Message {
		t.Fatalf("reason/message not preserved: got %q/%q", reason, message)
	}
	if runtimeID != sandbox.RuntimeID {
		t.Fatalf("runtime_id: got %q, want %q", runtimeID, sandbox.RuntimeID)
	}
	if specHash != sandbox.SpecHash {
		t.Fatalf("spec_hash: got %q, want %q", specHash, sandbox.SpecHash)
	}
	if revision != 1 {
		t.Fatalf("revision: got %d, want 1", revision)
	}
	if createdAt != sandbox.CreatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("created_at: got %q", createdAt)
	}
	if updatedAt != sandbox.UpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("updated_at: got %q", updatedAt)
	}
	if lastTransitionAt != sandbox.LastTransitionAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("last_transition_at: got %q", lastTransitionAt)
	}
}

// TestCreateSandboxDuplicateID 验证重复 ID 映射为领域冲突且不覆盖原记录。
func TestCreateSandboxDuplicateID(t *testing.T) {
	store := migrateTestStore(t)
	sandbox := createTestSandbox()

	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("first create: %v", err)
	}
	duplicate := sandbox
	duplicate.SpecHash = "different-hash"
	if err := store.Create(context.Background(), duplicate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate create: got %v, want ErrConflict", err)
	}

	var (
		count    int64
		specHash string
	)
	if err := store.db.QueryRow(
		"SELECT COUNT(*), spec_hash FROM sandboxes WHERE id = ?",
		sandbox.ID,
	).Scan(&count, &specHash); err != nil {
		t.Fatalf("read record after duplicate: %v", err)
	}
	if count != 1 || specHash != sandbox.SpecHash {
		t.Fatalf("duplicate changed record: count=%d spec_hash=%q", count, specHash)
	}
}

// TestCreateSandboxCanceledContext 验证已取消请求不会留下部分记录。
func TestCreateSandboxCanceledContext(t *testing.T) {
	store := migrateTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Create(ctx, createTestSandbox())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("create with canceled context: got %v, want context.Canceled", err)
	}

	var count int64
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sandboxes").Scan(&count); err != nil {
		t.Fatalf("count sandboxes: %v", err)
	}
	if count != 0 {
		t.Fatalf("canceled create left %d records", count)
	}
}
