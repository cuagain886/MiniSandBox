package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"
)

func migrateToV3(t *testing.T, store *Store) {
	t.Helper()
	if err := store.migrateWith(context.Background(), migrations[:3]); err != nil {
		t.Fatalf("migrate fixture to v3: %v", err)
	}
}

func insertRuntimeAnomaly(t *testing.T, store *Store, resourceKey, resourceType, classification, fingerprint string, observedAt time.Time) error {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO runtime_anomalies (
		resource_key, resource_type, classification, safe_fingerprint,
		first_seen_at, last_seen_at, observation_count, resolved_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, NULL)`,
		resourceKey,
		resourceType,
		classification,
		fingerprint,
		observedAt.UTC().Format(time.RFC3339Nano),
		observedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// TestMigrateV4AnomalyInsertUpsertResolve 验证同一安全 resource key 可去重
// observation、更新固定摘要并最终标记 resolved，而不会新增历史行。
func TestMigrateV4AnomalyInsertUpsertResolve(t *testing.T) {
	store := migrateTestStore(t)
	firstSeen := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	if err := insertRuntimeAnomaly(t, store, "sandbox.sbx-1.bundle", "sandbox_bundle", "incomplete_bundle", strings.Repeat("a", 64), firstSeen); err != nil {
		t.Fatalf("insert anomaly: %v", err)
	}
	secondSeen := firstSeen.Add(time.Minute)
	_, err := store.db.Exec(`INSERT INTO runtime_anomalies (
		resource_key, resource_type, classification, safe_fingerprint,
		first_seen_at, last_seen_at, observation_count, resolved_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, NULL)
	ON CONFLICT(resource_key) DO UPDATE SET
		resource_type = excluded.resource_type,
		classification = excluded.classification,
		safe_fingerprint = excluded.safe_fingerprint,
		last_seen_at = excluded.last_seen_at,
		observation_count = CASE
			WHEN runtime_anomalies.observation_count < 4294967295
			THEN runtime_anomalies.observation_count + 1
			ELSE runtime_anomalies.observation_count
		END,
		resolved_at = NULL`,
		"sandbox.sbx-1.bundle", "sandbox_bundle", "spec_hash_mismatch", strings.Repeat("b", 64),
		secondSeen.Format(time.RFC3339Nano), secondSeen.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("upsert anomaly: %v", err)
	}
	resolvedAt := secondSeen.Add(time.Minute)
	if _, err := store.db.Exec("UPDATE runtime_anomalies SET resolved_at = ? WHERE resource_key = ?",
		resolvedAt.Format(time.RFC3339Nano), "sandbox.sbx-1.bundle"); err != nil {
		t.Fatalf("resolve anomaly: %v", err)
	}
	var (
		classification string
		fingerprint    string
		firstText      string
		lastText       string
		count          uint32
		resolvedText   string
		rows           int
	)
	if err := store.db.QueryRow(`SELECT classification, safe_fingerprint,
		first_seen_at, last_seen_at, observation_count, resolved_at
		FROM runtime_anomalies WHERE resource_key = ?`, "sandbox.sbx-1.bundle").Scan(
		&classification, &fingerprint, &firstText, &lastText, &count, &resolvedText,
	); err != nil {
		t.Fatalf("read anomaly: %v", err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runtime_anomalies").Scan(&rows); err != nil {
		t.Fatalf("count anomalies: %v", err)
	}
	if rows != 1 || classification != "spec_hash_mismatch" || fingerprint != strings.Repeat("b", 64) || count != 2 {
		t.Fatalf("unexpected upsert result: rows=%d classification=%q fingerprint=%q count=%d", rows, classification, fingerprint, count)
	}
	if firstText != firstSeen.Format(time.RFC3339Nano) || lastText != secondSeen.Format(time.RFC3339Nano) || resolvedText != resolvedAt.Format(time.RFC3339Nano) {
		t.Fatalf("anomaly timestamps changed: first=%q last=%q resolved=%q", firstText, lastText, resolvedText)
	}
}

// TestMigrateV4AnomalyConstraints 锁定资源类型、分类、摘要、计数和时间约束。
func TestMigrateV4AnomalyConstraints(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	validFingerprint := strings.Repeat("a", 64)
	tests := []struct {
		name           string
		resourceKey    string
		resourceType   string
		classification string
		fingerprint    string
		firstSeen      string
		lastSeen       string
		count          int64
		resolved       any
	}{
		{"unsafe resource key", "host/path", "sandbox_bundle", "incomplete_bundle", validFingerprint, now, now, 1, nil},
		{"unknown resource type", "resource.1", "docker_log", "incomplete_bundle", validFingerprint, now, now, 1, nil},
		{"unknown classification", "resource.2", "main_container", "raw_error", validFingerprint, now, now, 1, nil},
		{"short fingerprint", "resource.3", "main_container", "identity_mismatch", "abc", now, now, 1, nil},
		{"uppercase fingerprint", "resource.4", "main_container", "identity_mismatch", strings.Repeat("A", 64), now, now, 1, nil},
		{"zero count", "resource.5", "runtime_directory", "lease_untrusted", validFingerprint, now, now, 0, nil},
		{"unbounded count", "resource.6", "workspace_volume", "duplicate_resource", validFingerprint, now, now, 4294967296, nil},
		{"offset first seen", "resource.7", "egress_sidecar", "network_namespace_mismatch", validFingerprint, "2026-08-12T00:00:00+08:00", now, 1, nil},
		{"last before first", "resource.8", "sandbox_bundle", "unknown_schema", validFingerprint, now, "2026-08-11T15:59:59Z", 1, nil},
		{"resolved before first", "resource.9", "sandbox_bundle", "security_profile_mismatch", validFingerprint, now, now, 1, "2026-08-11T15:59:59Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.db.Exec(`INSERT INTO runtime_anomalies (
				resource_key, resource_type, classification, safe_fingerprint,
				first_seen_at, last_seen_at, observation_count, resolved_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				test.resourceKey, test.resourceType, test.classification, test.fingerprint,
				test.firstSeen, test.lastSeen, test.count, test.resolved,
			)
			if err == nil {
				t.Fatal("invalid runtime anomaly was accepted")
			}
		})
	}
	if err := insertRuntimeAnomaly(t, store, "duplicate.key", "main_container", "duplicate_resource", validFingerprint, time.Now().UTC()); err != nil {
		t.Fatalf("insert duplicate baseline: %v", err)
	}
	if err := insertRuntimeAnomaly(t, store, "duplicate.key", "main_container", "duplicate_resource", validFingerprint, time.Now().UTC()); err == nil {
		t.Fatal("duplicate resource key was accepted without explicit upsert")
	}
}

// TestMigrateV4ReopenAndSafeSchema 验证 v4 重开幂等，且表只包含 allowlist 标量字段。
func TestMigrateV4ReopenAndSafeSchema(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open v3 database: %v", err)
	}
	migrateToV3(t, first)
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate v3 to v4: %v", err)
	}
	if err := insertRuntimeAnomaly(t, first, "reopen.anomaly", "runtime_directory", "lease_untrusted", strings.Repeat("c", 64), time.Now().UTC()); err != nil {
		t.Fatalf("insert reopen anomaly: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close v4 database: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen v4 database: %v", err)
	}
	defer second.Close()
	if err := second.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat v4 migration: %v", err)
	}
	if got := currentVersion(t, second); got != 4 {
		t.Fatalf("schema version: got %d, want 4", got)
	}
	if err := validateSchema(context.Background(), second.db, 4); err != nil {
		t.Fatalf("validate v4 schema: %v", err)
	}
	var reopenedClassification string
	if err := second.db.QueryRow("SELECT classification FROM runtime_anomalies WHERE resource_key = 'reopen.anomaly'").Scan(&reopenedClassification); err != nil || reopenedClassification != "lease_untrusted" {
		t.Fatalf("reopened anomaly mismatch: classification=%q err=%v", reopenedClassification, err)
	}
	var schemaSQL string
	if err := second.db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runtime_anomalies'").Scan(&schemaSQL); err != nil {
		t.Fatalf("read anomaly schema: %v", err)
	}
	lower := strings.ToLower(schemaSQL)
	for _, forbidden := range []string{" blob", " json", "path", "log", "environment", " env", "token", "secret", "raw"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("anomaly schema contains forbidden data shape %q: %s", forbidden, schemaSQL)
		}
	}
}
