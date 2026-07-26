package sqlite

import (
	"context"
	"testing"
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

// currentVersion 读取当前 schema 版本。
func currentVersion(t *testing.T, store *Store) int64 {
	t.Helper()

	var version int64
	if err := store.db.QueryRow(
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

// tableExists 判断给定名称的表或索引是否存在。
func tableExists(t *testing.T, store *Store, name string) bool {
	t.Helper()

	var count int64
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE name = ?",
		name,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return count > 0
}

// TestMigrateEmptyDatabase 验证空库迁移创建 5.4 节要求的表结构和索引。
func TestMigrateEmptyDatabase(t *testing.T) {
	store := openTestStore(t)

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate empty database: %v", err)
	}

	if got, want := currentVersion(t, store), int64(1); got != want {
		t.Fatalf("schema version: got %d, want %d", got, want)
	}
	if !tableExists(t, store, "sandboxes") {
		t.Fatal("sandboxes table missing")
	}
	if !tableExists(t, store, "idx_sandboxes_reconcile") {
		t.Fatal("reconcile index missing")
	}
	if tableExists(t, store, "idempotency_keys") {
		t.Fatal("phase 1 must not create idempotency_keys")
	}

	// 逐列核对 5.4 节要求的 sandboxes 表结构。
	rows, err := store.db.Query("PRAGMA table_info(sandboxes)")
	if err != nil {
		t.Fatalf("read table info: %v", err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var (
			cid          int64
			name         string
			columnType   string
			notNull      int64
			defaultValue any
			primaryKey   int64
		)
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table info: %v", err)
	}

	want := []string{
		"id",
		"spec_json",
		"desired_state",
		"observed_state",
		"reason",
		"message",
		"runtime_id",
		"spec_hash",
		"revision",
		"created_at",
		"updated_at",
		"last_transition_at",
	}
	for _, column := range want {
		if !columns[column] {
			t.Fatalf("sandboxes table missing column %s", column)
		}
	}
	if got, wantCount := len(columns), len(want); got != wantCount {
		t.Fatalf("unexpected column count: got %d, want %d", got, wantCount)
	}
}

// TestMigrateIdempotent 验证重复迁移不报错也不重复执行。
func TestMigrateIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	if got, want := currentVersion(t, store), int64(1); got != want {
		t.Fatalf("schema version after repeat: got %d, want %d", got, want)
	}

	var applied int64
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations",
	).Scan(&applied); err != nil {
		t.Fatalf("count applied migrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration recorded %d times, want 1", applied)
	}
}

// TestMigrateRejectsNewerDatabase 验证未知更高版本拒绝启动。
func TestMigrateRejectsNewerDatabase(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := store.db.Exec(
		"INSERT INTO schema_migrations (version, applied_at) VALUES (999, '')",
	); err != nil {
		t.Fatalf("simulate newer schema: %v", err)
	}

	if err := store.Migrate(ctx); err == nil {
		t.Fatal("expected error for newer database schema")
	}
}

// TestMigrateRollsBackOnFailure 验证失败迁移整体回滚，不留下半完成 schema。
func TestMigrateRollsBackOnFailure(t *testing.T) {
	t.Run("first migration fails", func(t *testing.T) {
		store := openTestStore(t)

		broken := []migration{
			{
				version: 1,
				statements: []string{
					"CREATE TABLE sandboxes (id TEXT PRIMARY KEY)",
					"THIS IS NOT VALID SQL",
				},
			},
		}
		if err := store.migrateWith(
			context.Background(),
			broken,
		); err == nil {
			t.Fatal("expected migration failure")
		}

		// 事务回滚后不应留下任何对象，包括版本表本身。
		if tableExists(t, store, "sandboxes") {
			t.Fatal("failed migration left sandboxes table")
		}
		if tableExists(t, store, "schema_migrations") {
			t.Fatal("failed migration left schema_migrations table")
		}
	})

	t.Run("later migration fails after applied version", func(t *testing.T) {
		store := openTestStore(t)
		ctx := context.Background()

		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("apply migration 1: %v", err)
		}

		broken := append(
			append([]migration{}, migrations...),
			migration{
				version:    2,
				statements: []string{"THIS IS NOT VALID SQL"},
			},
		)
		if err := store.migrateWith(ctx, broken); err == nil {
			t.Fatal("expected migration failure")
		}

		if got, want := currentVersion(t, store), int64(1); got != want {
			t.Fatalf(
				"version changed by failed migration: got %d, want %d",
				got,
				want,
			)
		}
	})
}

// TestMigrateRejectsInvalidList 验证乱序迁移列表在执行前被拒绝。
func TestMigrateRejectsInvalidList(t *testing.T) {
	store := openTestStore(t)

	invalid := []migration{
		{version: 2, statements: []string{"SELECT 1"}},
	}
	if err := store.migrateWith(context.Background(), invalid); err == nil {
		t.Fatal("expected error for invalid migration list")
	}
	if tableExists(t, store, "schema_migrations") {
		t.Fatal("invalid list must be rejected before any DDL")
	}
}
