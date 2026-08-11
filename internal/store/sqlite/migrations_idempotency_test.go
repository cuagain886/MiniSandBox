package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

const maxIdempotencyResponseBytes = 65_536

func insertIdempotencyRecord(t *testing.T, store *Store, scope, key, sandboxID string, response []byte, createdAt time.Time) error {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO idempotency_records (
		scope_id, idempotency_key, request_hash, sandbox_id, status_code,
		location, response_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		scope,
		key,
		strings.Repeat("a", 64),
		sandboxID,
		202,
		"/v1/sandboxes/"+sandboxID,
		response,
		createdAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func createMigrationSandbox(t *testing.T, store *Store, id string) domain.Sandbox {
	t.Helper()
	sandbox := createTestSandbox()
	sandbox.ID = id
	if err := store.Create(context.Background(), sandbox); err != nil {
		t.Fatalf("create sandbox %s: %v", id, err)
	}
	return sandbox
}

// TestMigrateV3IdempotencyConstraints 验证复合唯一键、scope 隔离、外键和
// 固定文本/响应边界，且 sandbox 删除不会级联清除重放记录。
func TestMigrateV3IdempotencyConstraints(t *testing.T) {
	store := migrateTestStore(t)
	sandbox := createMigrationSandbox(t, store, "idem-primary")
	now := time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC)
	if err := insertIdempotencyRecord(t, store, "local:v1", "key-1", sandbox.ID, []byte(`{"id":"idem-primary"}`), now); err != nil {
		t.Fatalf("insert valid idempotency record: %v", err)
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "key-1", sandbox.ID, []byte(`{}`), now); err == nil {
		t.Fatal("duplicate scope/key was accepted")
	}
	if err := insertIdempotencyRecord(t, store, "local:v2", "key-1", sandbox.ID, []byte(`{}`), now); err != nil {
		t.Fatalf("same key in another scope rejected: %v", err)
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "missing-sandbox", "missing", []byte(`{}`), now); err == nil {
		t.Fatal("missing sandbox foreign key was accepted")
	}
	if _, err := store.db.Exec("DELETE FROM sandboxes WHERE id = ?", sandbox.ID); err == nil {
		t.Fatal("sandbox deletion cascaded or bypassed idempotency foreign key")
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM idempotency_records WHERE sandbox_id = ?", sandbox.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("idempotency records were not retained: count=%d err=%v", count, err)
	}

	invalid := []struct {
		name  string
		scope string
		key   string
		hash  string
		time  string
	}{
		{"empty scope", "", "key-a", strings.Repeat("a", 64), now.Format(time.RFC3339Nano)},
		{"unsafe scope", "local/v1", "key-b", strings.Repeat("a", 64), now.Format(time.RFC3339Nano)},
		{"unsafe key", "local:v1", "key space", strings.Repeat("a", 64), now.Format(time.RFC3339Nano)},
		{"long key", "local:v1", strings.Repeat("k", 129), strings.Repeat("a", 64), now.Format(time.RFC3339Nano)},
		{"invalid hash", "local:v1", "key-c", strings.Repeat("g", 64), now.Format(time.RFC3339Nano)},
		{"offset time", "local:v1", "key-d", strings.Repeat("a", 64), "2026-08-11T21:00:00+08:00"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.db.Exec(`INSERT INTO idempotency_records (
				scope_id, idempotency_key, request_hash, sandbox_id, status_code,
				location, response_json, created_at
			) VALUES (?, ?, ?, ?, 202, ?, ?, ?)`,
				test.scope, test.key, test.hash, sandbox.ID,
				"/v1/sandboxes/"+sandbox.ID, []byte(`{}`), test.time,
			)
			if err == nil {
				t.Fatal("invalid idempotency record was accepted")
			}
		})
	}
}

// TestMigrateV3ResponseJSONLimit 锁定 response JSON 的有效性和 64 KiB 上限。
func TestMigrateV3ResponseJSONLimit(t *testing.T) {
	store := migrateTestStore(t)
	sandbox := createMigrationSandbox(t, store, "idem-response")
	now := time.Now().UTC()
	maxResponse := []byte(`{"d":"` + strings.Repeat("a", maxIdempotencyResponseBytes-8) + `"}`)
	if len(maxResponse) != maxIdempotencyResponseBytes {
		t.Fatalf("test response length is %d", len(maxResponse))
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "max-response", sandbox.ID, maxResponse, now); err != nil {
		t.Fatalf("maximum response rejected: %v", err)
	}
	tooLarge := []byte(`{"d":"` + strings.Repeat("a", maxIdempotencyResponseBytes-7) + `"}`)
	if err := insertIdempotencyRecord(t, store, "local:v1", "large-response", sandbox.ID, tooLarge, now); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "invalid-json", sandbox.ID, []byte(`{"id":`), now); err == nil {
		t.Fatal("invalid response JSON was accepted")
	}
}

// TestMigrateV3TerminalRetentionUsesSandboxTransition 验证 GC 候选依赖
// sandbox observed terminal 和 last_transition_at，而非 record created_at。
func TestMigrateV3TerminalRetentionUsesSandboxTransition(t *testing.T) {
	store := migrateTestStore(t)
	terminal := createMigrationSandbox(t, store, "idem-terminal")
	running := createMigrationSandbox(t, store, "idem-running")
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	oldTransition := now.Add(-48 * time.Hour)
	if _, err := store.db.Exec("UPDATE sandboxes SET desired_state = ?, observed_state = ?, last_transition_at = ? WHERE id = ?",
		domain.DesiredTerminated, domain.StateTerminated, oldTransition.Format(time.RFC3339Nano), terminal.ID); err != nil {
		t.Fatalf("mark terminal sandbox: %v", err)
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "new-record-old-terminal", terminal.ID, []byte(`{}`), now); err != nil {
		t.Fatalf("insert terminal record: %v", err)
	}
	if err := insertIdempotencyRecord(t, store, "local:v1", "old-record-running", running.ID, []byte(`{}`), now.Add(-72*time.Hour)); err != nil {
		t.Fatalf("insert running record: %v", err)
	}
	rows, err := store.db.Query(`SELECT i.idempotency_key
		FROM idempotency_records AS i
		JOIN sandboxes AS s ON s.id = i.sandbox_id
		WHERE s.observed_state = ? AND s.last_transition_at <= ?
		ORDER BY i.idempotency_key`, domain.StateTerminated, now.Add(-24*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("query terminal retention candidates: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan retention candidate: %v", err)
		}
		keys = append(keys, key)
	}
	if len(keys) != 1 || keys[0] != "new-record-old-terminal" {
		t.Fatalf("retention candidates: got %v", keys)
	}
}

// TestMigrateV3ReopenAndSchema 验证重复迁移、关闭重开、字段 allowlist 和关键索引。
func TestMigrateV3ReopenAndSchema(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	migrateToV2(t, first)
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate v2 to v3: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat v3 migration: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close v3 database: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen v3 database: %v", err)
	}
	defer second.Close()
	if err := second.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate reopened v3 database: %v", err)
	}
	if got := currentVersion(t, second); got != 3 {
		t.Fatalf("schema version: got %d, want 3", got)
	}
	if err := validateSchema(context.Background(), second.db, 3); err != nil {
		t.Fatalf("validate v3 schema: %v", err)
	}
	columns := map[string]bool{}
	rows, err := second.db.Query("PRAGMA table_info(idempotency_records)")
	if err != nil {
		t.Fatalf("read idempotency columns: %v", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int64
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan idempotency column: %v", err)
		}
		columns[name] = true
	}
	rows.Close()
	for _, forbidden := range []string{"authorization", "raw_request", "request_json", "token", "secret", "expires_at"} {
		if columns[forbidden] {
			t.Fatalf("forbidden idempotency column exists: %s", forbidden)
		}
	}
	backups, _ := filepath.Glob(path + ".pre-v2.*.bak")
	if len(backups) != 1 {
		t.Fatalf("v3 migration backup count: %v", backups)
	}
}
