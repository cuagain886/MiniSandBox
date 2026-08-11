package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

const migrationDefaultTTL = 30 * time.Minute

// migration 描述一次只向前的 schema 变更。
type migration struct {
	// version 是严格递增的迁移版本号，从 1 开始。
	version int64
	// apply 在外层 BEGIN IMMEDIATE 事务中执行本版本全部变更。
	apply func(context.Context, *sql.Conn, time.Time) error
}

// migrations 是全部已知迁移，追加新版本时必须保持版本严格递增。
//
// v1 是 Phase 2 最终 sandbox schema；v2 只增加 lease、retry、health 与
// origin 字段。idempotency 和 anomaly 表分别留给 v3、v4，不能混入本版本。
var migrations = []migration{
	{version: 1, apply: applyMigrationV1},
	{version: 2, apply: applyMigrationV2},
}

// Migrate 在可恢复备份之后，以 BEGIN IMMEDIATE 把 schema 升级到最新版本。
//
// 已存在的旧 schema 在任何 DDL 前使用 VACUUM INTO 生成同目录一致性备份；
// 备份失败则不开始迁移。迁移提交前后都验证版本、列和关键索引。重复调用
// 幂等，数据库版本高于当前二进制时拒绝继续。
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrateWith(ctx, migrations)
}

// migrateWith 用给定迁移列表执行迁移，供 Migrate 和失败回滚测试复用。
func (s *Store) migrateWith(ctx context.Context, list []migration) error {
	if err := validateMigrationList(list); err != nil {
		return err
	}

	current, err := schemaVersion(ctx, s.db)
	if err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}
	latest := int64(0)
	if len(list) > 0 {
		latest = list[len(list)-1].version
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, latest)
	}
	if current == latest {
		if current == 0 {
			return nil
		}
		return validateSchema(ctx, s.db, current)
	}
	migrationTime := s.now().UTC()
	if current > 0 {
		if err := validateSchema(ctx, s.db, current); err != nil {
			return fmt.Errorf("validate schema before migration: %w", err)
		}
		if _, err := s.backupBeforeMigration(ctx, current, migrationTime); err != nil {
			return err
		}
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}
	lockedCurrent, err := schemaVersion(ctx, conn)
	if err != nil {
		return fmt.Errorf("recheck schema version: %w", err)
	}
	if lockedCurrent != current {
		return errors.New("schema version changed before migration lock")
	}

	for _, item := range list {
		if item.version <= current {
			continue
		}
		if item.apply == nil {
			return fmt.Errorf("apply migration %d: missing implementation", item.version)
		}
		if err := item.apply(ctx, conn, migrationTime); err != nil {
			return fmt.Errorf("apply migration %d: %w", item.version, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			item.version,
			migrationTime.Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
	}
	if err := validateSchema(ctx, conn, latest); err != nil {
		return fmt.Errorf("validate schema before commit: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true
	if err := validateSchema(ctx, conn, latest); err != nil {
		return fmt.Errorf("validate schema after commit: %w", err)
	}
	return nil
}

// backupBeforeMigration 生成不可覆盖的同目录升级前备份并返回其路径。
func (s *Store) backupBeforeMigration(ctx context.Context, version int64, migrationTime time.Time) (string, error) {
	stamp := migrationTime.UnixNano()
	var backupPath string
	for sequence := 0; ; sequence++ {
		backupPath = fmt.Sprintf("%s.pre-v%d.%d.%d.bak", s.path, version, stamp, sequence)
		if _, err := os.Lstat(backupPath); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", fmt.Errorf("inspect migration backup path: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return "", fmt.Errorf("create pre-migration backup: %w", err)
	}
	info, err := os.Lstat(backupPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("pre-migration backup is not a regular file")
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return "", fmt.Errorf("restrict pre-migration backup permissions: %w", err)
	}
	return backupPath, nil
}

// applyMigrationV1 创建 Phase 2 最终 sandbox 表和收敛索引。
func applyMigrationV1(ctx context.Context, conn *sql.Conn, _ time.Time) error {
	statements := []string{
		`CREATE TABLE sandboxes (
			id TEXT PRIMARY KEY,
			spec_json BLOB NOT NULL,
			desired_state TEXT NOT NULL,
			observed_state TEXT NOT NULL,
			reason TEXT NOT NULL,
			message TEXT NOT NULL,
			runtime_id TEXT NOT NULL DEFAULT '',
			spec_hash TEXT NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_transition_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_sandboxes_reconcile ON sandboxes (desired_state, observed_state)`,
	}
	return executeMigrationStatements(ctx, conn, statements)
}

// applyMigrationV2 重建 sandbox 表并按一次 migration clock 回填可靠性字段。
func applyMigrationV2(ctx context.Context, conn *sql.Conn, migrationTime time.Time) error {
	if err := executeMigrationStatements(ctx, conn, []string{
		`CREATE TABLE sandboxes_v2 (
			id TEXT PRIMARY KEY,
			spec_json BLOB NOT NULL,
			desired_state TEXT NOT NULL,
			observed_state TEXT NOT NULL,
			reason TEXT NOT NULL,
			message TEXT NOT NULL,
			runtime_id TEXT NOT NULL DEFAULT '',
			spec_hash TEXT NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_transition_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			retry_attempt INTEGER NOT NULL CHECK (retry_attempt BETWEEN 0 AND 4294967295),
			next_reconcile_at TEXT,
			last_reconcile_at TEXT,
			health_failure_count INTEGER NOT NULL CHECK (health_failure_count BETWEEN 0 AND 4294967295),
			origin TEXT NOT NULL CHECK (origin IN ('api', 'recovered_orphan'))
		)`,
	}); err != nil {
		return err
	}
	nowText := migrationTime.Format(time.RFC3339Nano)
	runningExpiry := migrationTime.Add(migrationDefaultTTL).Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(ctx, `INSERT INTO sandboxes_v2 (
		id, spec_json, desired_state, observed_state, reason, message, runtime_id,
		spec_hash, revision, created_at, updated_at, last_transition_at, expires_at,
		retry_attempt, next_reconcile_at, last_reconcile_at, health_failure_count, origin
	) SELECT
		id, spec_json, desired_state, observed_state, reason, message, runtime_id,
		spec_hash, revision, created_at, updated_at, last_transition_at,
		CASE
			WHEN observed_state = ? THEN last_transition_at
			WHEN desired_state = ? THEN ?
			ELSE ?
		END,
		0, NULL, NULL, 0, ?
	FROM sandboxes`, "Terminated", "Terminated", nowText, runningExpiry, "api"); err != nil {
		return err
	}
	return executeMigrationStatements(ctx, conn, []string{
		`DROP TABLE sandboxes`,
		`ALTER TABLE sandboxes_v2 RENAME TO sandboxes`,
		`CREATE INDEX idx_sandboxes_reconcile ON sandboxes (desired_state, observed_state)`,
	})
}

func executeMigrationStatements(ctx context.Context, conn *sql.Conn, statements []string) error {
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func schemaVersion(ctx context.Context, queryer schemaQueryer) (int64, error) {
	var exists int
	if err := queryer.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'",
	).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version int64
	if err := queryer.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func validateSchema(ctx context.Context, queryer schemaQueryer, version int64) error {
	actualVersion, err := schemaVersion(ctx, queryer)
	if err != nil {
		return err
	}
	if actualVersion != version {
		return fmt.Errorf("schema version is %d, want %d", actualVersion, version)
	}
	var migrationRows int64
	if err := queryer.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationRows); err != nil {
		return err
	}
	if migrationRows != version {
		return fmt.Errorf("schema migration row count is %d, want %d", migrationRows, version)
	}
	wantColumns := []string{
		"id", "spec_json", "desired_state", "observed_state", "reason", "message",
		"runtime_id", "spec_hash", "revision", "created_at", "updated_at", "last_transition_at",
	}
	if version >= 2 {
		wantColumns = append(wantColumns, "expires_at", "retry_attempt", "next_reconcile_at", "last_reconcile_at", "health_failure_count", "origin")
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA table_info(sandboxes)")
	if err != nil {
		return err
	}
	defer rows.Close()
	actualColumns := make([]string, 0, len(wantColumns))
	for rows.Next() {
		var cid, notNull, primaryKey int64
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		actualColumns = append(actualColumns, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(actualColumns) != len(wantColumns) {
		return fmt.Errorf("sandboxes column count is %d, want %d", len(actualColumns), len(wantColumns))
	}
	for index := range wantColumns {
		if actualColumns[index] != wantColumns[index] {
			return fmt.Errorf("sandboxes column %d is unexpected", index)
		}
	}
	var indexCount int
	if err := queryer.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sandboxes_reconcile' AND tbl_name = 'sandboxes'",
	).Scan(&indexCount); err != nil {
		return err
	}
	if indexCount != 1 {
		return errors.New("required sandbox reconcile index is missing")
	}
	return nil
}

// validateMigrationList 校验迁移列表从 1 开始且版本严格递增。
func validateMigrationList(list []migration) error {
	expected := int64(1)
	for _, item := range list {
		if item.version != expected {
			return errors.New("migration list must start at 1 and increase strictly")
		}
		expected++
	}
	return nil
}
