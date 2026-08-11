package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// openTestStore 打开临时数据库并注册清理。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(testDatabasePath(t))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func currentVersion(t *testing.T, store *Store) int64 {
	t.Helper()
	version, err := schemaVersion(context.Background(), store.db)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func tableExists(t *testing.T, store *Store, name string) bool {
	t.Helper()
	var count int64
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return count > 0
}

func sandboxColumns(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.db.Query("PRAGMA table_info(sandboxes)")
	if err != nil {
		t.Fatalf("read table info: %v", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int64
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table info: %v", err)
	}
	return columns
}

func migrateToV1(t *testing.T, store *Store) {
	t.Helper()
	if err := store.migrateWith(context.Background(), migrations[:1]); err != nil {
		t.Fatalf("migrate fixture to v1: %v", err)
	}
}

func migrateToV2(t *testing.T, store *Store) {
	t.Helper()
	if err := store.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("migrate fixture to v2: %v", err)
	}
}

// TestMigrateEmptyDatabase 验证空库直接建立 v2 schema，且不提前创建后续表。
func TestMigrateEmptyDatabase(t *testing.T) {
	store := openTestStore(t)
	migrateToV2(t, store)
	if got, want := currentVersion(t, store), int64(2); got != want {
		t.Fatalf("schema version: got %d, want %d", got, want)
	}
	if err := validateSchema(context.Background(), store.db, 2); err != nil {
		t.Fatalf("validate v2 schema: %v", err)
	}
	for _, absent := range []string{"idempotency_records", "runtime_anomalies"} {
		if tableExists(t, store, absent) {
			t.Fatalf("P3-010 must not create %s", absent)
		}
	}
}

// TestMigratePhase2FixtureBackfillsEveryState 使用真实 v1 表验证所有旧状态的
// expiry、retry、health、origin 回填，并确认原 spec/revision/runtime/state 不变。
func TestMigratePhase2FixtureBackfillsEveryState(t *testing.T) {
	store := openTestStore(t)
	migrateToV1(t, store)
	migrationTime := time.Date(2026, 8, 11, 12, 30, 0, 123456789, time.UTC)
	clockCalls := 0
	store.now = func() time.Time {
		clockCalls++
		return migrationTime
	}

	states := []domain.SandboxState{
		domain.StatePending,
		domain.StateCreating,
		domain.StateRunning,
		domain.StateStopping,
		domain.StateTerminated,
		domain.StateFailed,
	}
	lastTransitions := make(map[string]time.Time, len(states))
	desiredStates := make(map[string]domain.DesiredState, len(states))
	for index, state := range states {
		id := fmt.Sprintf("phase2-%02d", index)
		desired := domain.DesiredRunning
		if state == domain.StateStopping || state == domain.StateTerminated {
			desired = domain.DesiredTerminated
		}
		lastTransition := migrationTime.Add(-time.Duration(index+1) * time.Hour)
		lastTransitions[id] = lastTransition
		desiredStates[id] = desired
		insertPhase2Sandbox(t, store, id, desired, state, uint64(index+7), "runtime-"+id, lastTransition)
	}

	if err := store.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("upgrade Phase 2 fixture: %v", err)
	}
	if clockCalls != 1 {
		t.Fatalf("migration clock called %d times, want exactly once", clockCalls)
	}
	if got := currentVersion(t, store); got != 2 {
		t.Fatalf("schema version after upgrade: got %d", got)
	}
	for index, state := range states {
		id := fmt.Sprintf("phase2-%02d", index)
		got, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("read upgraded %s: %v", id, err)
		}
		if got.DesiredState != desiredStates[id] || got.ObservedState != state || got.Revision != uint64(index+7) || got.RuntimeID != "runtime-"+id {
			t.Fatalf("legacy fields changed for %s: %+v", id, got)
		}
		wantSpec := domain.SandboxSpec{
			Image:     "busybox:1.36",
			Resources: domain.ResourceLimits{CPUQuotaMillis: 500, MemoryMiB: 256, PIDs: 64},
			Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath},
			Platform:  domain.Platform{OS: "linux", Arch: "amd64"},
		}
		if !reflect.DeepEqual(got.Spec, wantSpec) || got.SpecHash != "spec-hash-"+id || got.Reason != "FIXTURE" || got.Message != "fixture" {
			t.Fatalf("spec fields changed for %s: %+v", id, got)
		}
		if !got.CreatedAt.Equal(lastTransitions[id].Add(-time.Hour)) || !got.UpdatedAt.Equal(lastTransitions[id]) || !got.LastTransitionAt.Equal(lastTransitions[id]) {
			t.Fatalf("legacy timestamps changed for %s: %+v", id, got)
		}
		wantExpiry := migrationTime.Add(migrationDefaultTTL)
		if state == domain.StateStopping {
			wantExpiry = migrationTime
		}
		if state == domain.StateTerminated {
			wantExpiry = lastTransitions[id]
		}
		if got.ExpiresAt == nil || !got.ExpiresAt.Equal(wantExpiry) {
			t.Fatalf("expiry for %s: got %v, want %s", id, got.ExpiresAt, wantExpiry)
		}
		if got.RetryAttempt != 0 || got.NextReconcileAt != nil || got.LastReconcileAt != nil || got.HealthFailureCount != 0 || got.Origin != domain.SandboxOriginAPI {
			t.Fatalf("v2 metadata not safely backfilled for %s: %+v", id, got)
		}
	}

	backups, err := filepath.Glob(store.path + ".pre-v1.*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-v2 backup count: paths=%v err=%v", backups, err)
	}
	backup, err := Open(backups[0])
	if err != nil {
		t.Fatalf("open consistent backup: %v", err)
	}
	defer backup.Close()
	if err := validateSchema(context.Background(), backup.db, 1); err != nil {
		t.Fatalf("backup is not readable Phase 2 schema: %v", err)
	}
}

func insertPhase2Sandbox(t *testing.T, store *Store, id string, desired domain.DesiredState, observed domain.SandboxState, revision uint64, runtimeID string, lastTransition time.Time) {
	t.Helper()
	const specJSON = `{"image":"busybox:1.36","resources":{"cpu_quota_millis":500,"memory_mib":256,"pids":64},"workspace":{"mount_path":"/workspace","persistent":false},"network":{"outbound":false},"platform":{"os":"linux","arch":"amd64"}}`
	_, err := store.db.Exec(`INSERT INTO sandboxes (
		id, spec_json, desired_state, observed_state, reason, message, runtime_id,
		spec_hash, revision, created_at, updated_at, last_transition_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, []byte(specJSON), desired, observed, "FIXTURE", "fixture", runtimeID,
		"spec-hash-"+id, revision,
		lastTransition.Add(-time.Hour).Format(time.RFC3339Nano),
		lastTransition.Format(time.RFC3339Nano),
		lastTransition.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert Phase 2 fixture %s: %v", id, err)
	}
}

// TestMigrateBackupFailureDoesNotStartUpgrade 验证 VACUUM INTO 失败时 v1 原样保留。
func TestMigrateBackupFailureDoesNotStartUpgrade(t *testing.T) {
	store := openTestStore(t)
	migrateToV1(t, store)
	insertPhase2Sandbox(t, store, "kept", domain.DesiredRunning, domain.StateRunning, 9, "runtime-kept", time.Now().UTC())
	store.path = filepath.Join(t.TempDir(), "missing", "sandboxd.db")

	if err := store.migrateWith(context.Background(), migrations[:2]); err == nil {
		t.Fatal("expected backup failure")
	}
	if got := currentVersion(t, store); got != 1 {
		t.Fatalf("backup failure changed schema version: %d", got)
	}
	if got := len(sandboxColumns(t, store)); got != 12 {
		t.Fatalf("backup failure changed v1 columns: %d", got)
	}
}

// TestMigrateReopenIdempotent 验证关闭重开后重复迁移不再创建备份或版本行。
func TestMigrateReopenIdempotent(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	migrateToV1(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("close v1 store: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen v1 store: %v", err)
	}
	if err := second.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("upgrade reopened store: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close v2 store: %v", err)
	}
	third, err := Open(path)
	if err != nil {
		t.Fatalf("reopen v2 store: %v", err)
	}
	defer third.Close()
	if err := third.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var applied int
	if err := third.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil || applied != 2 {
		t.Fatalf("migration rows: got %d err=%v", applied, err)
	}
	backups, _ := filepath.Glob(path + ".pre-v1.*.bak")
	if len(backups) != 1 {
		t.Fatalf("idempotent reopen created extra backups: %v", backups)
	}
}

// TestMigrateInterruptedUpgradeRollsBack 验证 v2 后续步骤失败会把整个
// BEGIN IMMEDIATE 事务回滚到可重试 v1，而备份仍可用于停机恢复。
func TestMigrateInterruptedUpgradeRollsBack(t *testing.T) {
	store := openTestStore(t)
	migrateToV1(t, store)
	insertPhase2Sandbox(t, store, "rollback", domain.DesiredRunning, domain.StateCreating, 11, "runtime-rollback", time.Now().UTC())
	broken := append([]migration{}, migrations[:2]...)
	broken = append(broken, migration{version: 3, apply: func(ctx context.Context, conn *sql.Conn, _ time.Time) error {
		if _, err := conn.ExecContext(ctx, "CREATE TABLE must_rollback (id INTEGER)"); err != nil {
			return err
		}
		return errors.New("simulated interruption")
	}})
	if err := store.migrateWith(context.Background(), broken); err == nil {
		t.Fatal("expected interrupted migration failure")
	}
	if got := currentVersion(t, store); got != 1 || len(sandboxColumns(t, store)) != 12 {
		t.Fatalf("interrupted migration did not restore v1: version=%d columns=%v", got, sandboxColumns(t, store))
	}
	if tableExists(t, store, "must_rollback") {
		t.Fatal("interrupted migration left later schema object")
	}
	var revision uint64
	if err := store.db.QueryRow("SELECT revision FROM sandboxes WHERE id = 'rollback'").Scan(&revision); err != nil || revision != 11 {
		t.Fatalf("legacy row changed after rollback: revision=%d err=%v", revision, err)
	}
	if err := store.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("retry migration after rollback: %v", err)
	}
}

func TestMigrateRejectsNewerDatabase(t *testing.T) {
	store := openTestStore(t)
	if err := store.migrateWith(context.Background(), migrations[:2]); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := store.db.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (999, '')"); err != nil {
		t.Fatalf("simulate newer schema: %v", err)
	}
	if err := store.Migrate(context.Background()); err == nil {
		t.Fatal("expected error for newer database schema")
	}
}

func TestMigrateFirstVersionFailureRollsBack(t *testing.T) {
	store := openTestStore(t)
	broken := []migration{{version: 1, apply: func(ctx context.Context, conn *sql.Conn, _ time.Time) error {
		if _, err := conn.ExecContext(ctx, "CREATE TABLE sandboxes (id TEXT PRIMARY KEY)"); err != nil {
			return err
		}
		return errors.New("simulated first migration failure")
	}}}
	if err := store.migrateWith(context.Background(), broken); err == nil {
		t.Fatal("expected migration failure")
	}
	if tableExists(t, store, "sandboxes") || tableExists(t, store, "schema_migrations") {
		t.Fatal("failed first migration left schema objects")
	}
}

func TestMigrateRejectsInvalidList(t *testing.T) {
	store := openTestStore(t)
	if err := store.migrateWith(context.Background(), []migration{{version: 2}}); err == nil {
		t.Fatal("expected error for invalid migration list")
	}
	if tableExists(t, store, "schema_migrations") {
		t.Fatal("invalid list must be rejected before any DDL")
	}
}

// TestBackupFileIsMaterialized guards against a driver silently accepting VACUUM INTO
// without creating a regular backup file on the host filesystem.
func TestBackupFileIsMaterialized(t *testing.T) {
	store := openTestStore(t)
	migrateToV1(t, store)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate with backup: %v", err)
	}
	paths, _ := filepath.Glob(store.path + ".pre-v1.*.bak")
	if len(paths) != 1 {
		t.Fatalf("backup path count: %v", paths)
	}
	info, err := os.Lstat(paths[0])
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("backup is not a non-empty regular file: info=%v err=%v", info, err)
	}
}
