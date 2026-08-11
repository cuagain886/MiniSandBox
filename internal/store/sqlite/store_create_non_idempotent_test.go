package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// nonIdempotentSandbox 返回与 keyed create 相同初始不变量但没有重放身份的记录。
func nonIdempotentSandbox(id string) domain.Sandbox {
	request := idempotentCreateRequest(id, "unused-key")
	return request.Sandbox
}

// TestCreateNonIdempotentAlwaysCreatesDistinctSandbox 验证相同语义的连续请求不被合并。
func TestCreateNonIdempotentAlwaysCreatesDistinctSandbox(t *testing.T) {
	store := migrateTestStore(t)
	for _, id := range []string{"no-key-one", "no-key-two"} {
		created, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{Sandbox: nonIdempotentSandbox(id), MaxSandboxes: 100})
		if err != nil || created.ID != id || created.Revision != 1 {
			t.Fatalf("create %s: %#v/%v", id, created, err)
		}
	}
	assertIdempotentCounts(t, store, 2, 0)
}

// TestCreateNonIdempotentConcurrentRequests 验证并发相同语义仍各自创建且不写重放表。
func TestCreateNonIdempotentConcurrentRequests(t *testing.T) {
	store := migrateTestStore(t)
	const count = 8
	start := make(chan struct{})
	errorsByIndex := make([]error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByIndex[index] = store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
				Sandbox: nonIdempotentSandbox(fmt.Sprintf("no-key-%d", index)), MaxSandboxes: 100,
			})
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("concurrent create %d: %v", index, err)
		}
	}
	assertIdempotentCounts(t, store, count, 0)
}

// TestCreateNonIdempotentRollback 验证 INSERT 或 COMMIT 失败不留下 sandbox。
func TestCreateNonIdempotentRollback(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		store := migrateTestStore(t)
		if _, err := store.db.Exec(`CREATE TRIGGER reject_sandbox_insert
			BEFORE INSERT ON sandboxes BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		if _, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{Sandbox: nonIdempotentSandbox("insert-fail"), MaxSandboxes: 100}); err == nil {
			t.Fatal("insert failure ignored")
		}
		assertIdempotentCounts(t, store, 0, 0)
	})
	t.Run("commit", func(t *testing.T) {
		store := migrateTestStore(t)
		injected := errors.New("injected commit failure")
		store.commitImmediate = func(context.Context, *sql.Conn) error { return injected }
		if _, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{Sandbox: nonIdempotentSandbox("commit-fail"), MaxSandboxes: 100}); !errors.Is(err, injected) {
			t.Fatalf("commit failure: %v", err)
		}
		assertIdempotentCounts(t, store, 0, 0)
	})
}
