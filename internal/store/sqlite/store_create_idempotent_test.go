package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// idempotentCreateRequest 返回包含显式 expiry 和首次 202 bytes 的合法请求。
func idempotentCreateRequest(id, key string) storeport.IdempotentCreateRequest {
	sandbox := createTestSandbox()
	sandbox.ID = id
	sandbox.Revision = 0
	sandbox.Origin = domain.SandboxOriginAPI
	createdAt := sandbox.CreatedAt.UTC()
	body := []byte(`{"id":"` + id + `","state":"Pending","reason":"CREATE_ACCEPTED","message":"Sandbox creation has been accepted.","image":"busybox:1.36","expires_at":"` + sandbox.ExpiresAt.UTC().Format(time.RFC3339Nano) + `","created_at":"` + createdAt.Format(time.RFC3339Nano) + `","updated_at":"` + createdAt.Format(time.RFC3339Nano) + `"}`)
	return storeport.IdempotentCreateRequest{
		ScopeID:     "local:v1",
		Key:         key,
		RequestHash: strings.Repeat("a", 64),
		Sandbox:     sandbox,
		Response: storeport.IdempotentResponse{
			SchemaVersion: idempotencyResponseSchemaVersion,
			StatusCode:    202,
			Location:      "/v1/sandboxes/" + id,
			Body:          body,
			CreatedAt:     createdAt,
		},
		MaxSandboxes: 100,
	}
}

// TestCreateIdempotentCommitsBothRecords 验证 sandbox 和首次响应在同一事务可见。
func TestCreateIdempotentCommitsBothRecords(t *testing.T) {
	store := migrateTestStore(t)
	request := idempotentCreateRequest("idem-create", "request-1")
	result, err := store.CreateIdempotent(context.Background(), request)
	if err != nil {
		t.Fatalf("create idempotent: %v", err)
	}
	if result.Replayed || result.Sandbox.ID != request.Sandbox.ID || result.Sandbox.Revision != 1 ||
		result.Response.StatusCode != 202 || string(result.Response.Body) != string(request.Response.Body) {
		t.Fatalf("create result: %#v", result)
	}
	request.Response.Body[2] = 'X'
	if string(result.Response.Body) == string(request.Response.Body) {
		t.Fatal("result reused caller response backing array")
	}
	assertIdempotentCounts(t, store, 1, 1)
}

// TestCreateIdempotentChecksExistingKeyBeforeSandboxInsert 验证已有相同请求不制造第二条 sandbox。
func TestCreateIdempotentChecksExistingKeyBeforeSandboxInsert(t *testing.T) {
	store := migrateTestStore(t)
	first := idempotentCreateRequest("idem-first", "same-key")
	if _, err := store.CreateIdempotent(context.Background(), first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	second := idempotentCreateRequest("idem-second", "same-key")
	replayed, err := store.CreateIdempotent(context.Background(), second)
	if err != nil || !replayed.Replayed || replayed.Sandbox.ID != first.Sandbox.ID {
		t.Fatalf("existing key replay: %#v/%v", replayed, err)
	}
	assertIdempotentCounts(t, store, 1, 1)
}

// TestCreateIdempotentRollsBackSandboxInsertFailure 验证 sandbox 冲突不会留下重放记录。
func TestCreateIdempotentRollsBackSandboxInsertFailure(t *testing.T) {
	store := migrateTestStore(t)
	request := idempotentCreateRequest("duplicate-sandbox", "new-key")
	if err := store.Create(context.Background(), request.Sandbox); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}
	if _, err := store.CreateIdempotent(context.Background(), request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate sandbox: got %v, want conflict", err)
	}
	assertIdempotentCounts(t, store, 1, 0)
}

// TestCreateIdempotentRollsBackIdempotencyInsertFailure 验证第二次 INSERT 失败会撤销 sandbox。
func TestCreateIdempotentRollsBackIdempotencyInsertFailure(t *testing.T) {
	store := migrateTestStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER reject_idempotency_insert
		BEFORE INSERT ON idempotency_records BEGIN
			SELECT RAISE(ABORT, 'injected idempotency failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if _, err := store.CreateIdempotent(context.Background(), idempotentCreateRequest("rollback-record", "rollback-key")); err == nil {
		t.Fatal("injected idempotency failure was ignored")
	}
	assertIdempotentCounts(t, store, 0, 0)
}

// TestCreateIdempotentRollsBackCommitFailure 验证 COMMIT 失败后两个 INSERT 都不可见。
func TestCreateIdempotentRollsBackCommitFailure(t *testing.T) {
	store := migrateTestStore(t)
	injected := errors.New("injected commit failure")
	store.commitImmediate = func(context.Context, *sql.Conn) error { return injected }
	if _, err := store.CreateIdempotent(context.Background(), idempotentCreateRequest("commit-fail", "commit-key")); !errors.Is(err, injected) {
		t.Fatalf("commit failure: got %v", err)
	}
	assertIdempotentCounts(t, store, 0, 0)
}

// TestCreateIdempotentReopen 验证提交结果关闭重开后仍同时存在。
func TestCreateIdempotentReopen(t *testing.T) {
	path := testDatabasePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	request := idempotentCreateRequest("idem-reopen", "reopen-key")
	if _, err := first.CreateIdempotent(context.Background(), request); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	assertIdempotentCounts(t, second, 1, 1)
	if got, err := second.Get(context.Background(), request.Sandbox.ID); err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(*request.Sandbox.ExpiresAt) {
		t.Fatalf("reopened sandbox: %#v/%v", got, err)
	}
}

// TestCreateIdempotentRejectsInvalidEnvelope 验证非 202、未知 schema 和无 expiry 在事务前失败。
func TestCreateIdempotentRejectsInvalidEnvelope(t *testing.T) {
	store := migrateTestStore(t)
	for _, mutate := range []func(*storeport.IdempotentCreateRequest){
		func(request *storeport.IdempotentCreateRequest) { request.Response.StatusCode = 200 },
		func(request *storeport.IdempotentCreateRequest) { request.Response.SchemaVersion = 2 },
		func(request *storeport.IdempotentCreateRequest) { request.Sandbox.ExpiresAt = nil },
		func(request *storeport.IdempotentCreateRequest) { request.Response.Body = []byte(`invalid`) },
	} {
		request := idempotentCreateRequest("invalid-envelope", "invalid-key")
		mutate(&request)
		if _, err := store.CreateIdempotent(context.Background(), request); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid request: got %v, want ErrInvalid", err)
		}
		assertIdempotentCounts(t, store, 0, 0)
	}
}

// assertIdempotentCounts 验证数据库不存在半提交状态。
func assertIdempotentCounts(t *testing.T, store *Store, wantSandboxes, wantRecords int) {
	t.Helper()
	var sandboxes, records int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sandboxes").Scan(&sandboxes); err != nil {
		t.Fatalf("count sandboxes: %v", err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM idempotency_records").Scan(&records); err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}
	if sandboxes != wantSandboxes || records != wantRecords {
		t.Fatalf("record counts: sandbox=%d/%d idempotency=%d/%d", sandboxes, wantSandboxes, records, wantRecords)
	}
}
