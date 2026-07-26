package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// testDatabasePath 返回临时目录中的数据库路径。
func testDatabasePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sandboxd.db")
}

// TestOpenPingClose 验证 Open 建立真实连接、PRAGMA 生效且可关闭。
func TestOpenPingClose(t *testing.T) {
	path := testDatabasePath(t)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	// Open 已经完成 Ping 与 PRAGMA 校验，这里复核可以直接执行查询。
	var one int64
	if err := store.db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query after open: %v", err)
	}
	if one != 1 {
		t.Fatalf("unexpected query result: %d", one)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file missing after open: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close is not idempotent: %v", err)
	}
}

// TestOpenAppliesPragmas 验证 WAL、外键与 busy timeout 实际生效。
func TestOpenAppliesPragmas(t *testing.T) {
	store, err := Open(testDatabasePath(t))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer store.Close()

	var journalMode string
	if err := store.db.QueryRow(
		"PRAGMA journal_mode",
	).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode: got %q, want wal", journalMode)
	}

	var foreignKeys int64
	if err := store.db.QueryRow(
		"PRAGMA foreign_keys",
	).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys: got %d, want 1", foreignKeys)
	}

	var busyTimeout int64
	if err := store.db.QueryRow(
		"PRAGMA busy_timeout",
	).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != busyTimeoutMillis {
		t.Fatalf(
			"busy_timeout: got %d, want %d",
			busyTimeout,
			busyTimeoutMillis,
		)
	}
}

// TestReopenSameDatabase 验证同一数据库文件可以关闭后再次打开。
func TestReopenSameDatabase(t *testing.T) {
	path := testDatabasePath(t)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := first.db.Exec(
		"CREATE TABLE reopen_probe (id INTEGER PRIMARY KEY)",
	); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first connection: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer second.Close()

	var name string
	if err := second.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE name = 'reopen_probe'",
	).Scan(&name); err != nil {
		t.Fatalf("probe table lost after reopen: %v", err)
	}
}

// TestOpenRejectsURISpecialCharacters 验证含 URI 特殊字符的路径被拒绝,
// 而不是把数据库静默打开到错误位置。
func TestOpenRejectsURISpecialCharacters(t *testing.T) {
	for _, character := range []string{"#", "%", "?"} {
		path := filepath.Join(t.TempDir(), "data"+character+"1", "sandboxd.db")
		if _, err := Open(path); err == nil {
			t.Fatalf("expected error for path containing %q", character)
		}
	}
}

// TestOpenMissingParentDirectory 验证父目录缺失时报错而不是静默创建。
func TestOpenMissingParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "sandboxd.db")

	if _, err := Open(path); err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

// TestRemainingCRUDStillNotImplemented 验证 P1-017 范围外的方法仍显式返回未实现错误。
func TestRemainingCRUDStillNotImplemented(t *testing.T) {
	store, err := Open(testDatabasePath(t))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	calls := map[string]func() error{
		"UpdateDesired": func() error {
			_, err := store.UpdateDesired(ctx, "sb-1", domain.DesiredRunning, 1)
			return err
		},
		"UpdateObserved": func() error {
			_, err := store.UpdateObserved(ctx, storeport.ObservedUpdate{})
			return err
		},
		"ListReconcileCandidates": func() error {
			_, err := store.ListReconcileCandidates(ctx, 10)
			return err
		},
		"ListAll": func() error {
			_, err := store.ListAll(ctx)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, domain.ErrNotImplemented) {
			t.Fatalf("%s: got %v, want ErrNotImplemented", name, err)
		}
	}
}
