package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestSandboxQuotaConcurrentDeleteReleaseAndReplay 验证满额时幂等重放不占第二份容量，
// Terminated 提交释放容量后并发新建仍严格受事务上限约束。
func TestSandboxQuotaConcurrentDeleteReleaseAndReplay(t *testing.T) {
	store := migrateTestStore(t)
	const limit = 4
	original := make([]storeport.IdempotentCreateRequest, limit)
	for index := range limit {
		request := idempotentCreateRequest(fmt.Sprintf("quota-original-%d", index), fmt.Sprintf("quota-key-%d", index))
		request.MaxSandboxes = limit
		if _, err := store.CreateIdempotent(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		original[index] = request
	}

	// full quota 下 replay 必须先命中身份，既成功又不新增资源。
	for index, request := range original {
		replay := idempotentCreateRequest(fmt.Sprintf("quota-discarded-%d", index), request.Key)
		replay.RequestHash, replay.MaxSandboxes = request.RequestHash, limit
		result, err := store.CreateIdempotent(context.Background(), replay)
		if err != nil || !result.Replayed || result.Sandbox.ID != request.Sandbox.ID {
			t.Fatalf("full replay %d: %#v/%v", index, result, err)
		}
	}

	if _, err := store.db.Exec("UPDATE sandboxes SET desired_state = ?, observed_state = ? WHERE id = ?",
		domain.DesiredTerminated, domain.StateTerminated, original[0].Sandbox.ID); err != nil {
		t.Fatal(err)
	}
	const contenders = 20
	start := make(chan struct{})
	errs := make([]error, contenders)
	var wait sync.WaitGroup
	for index := range contenders {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errs[index] = store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
				Sandbox: nonIdempotentSandbox(fmt.Sprintf("quota-contender-%02d", index)), MaxSandboxes: limit,
			})
		}(index)
	}
	close(start)
	wait.Wait()
	successes, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrSandboxLimitReached):
			limited++
		default:
			t.Fatalf("contender: %v", err)
		}
	}
	if successes != 1 || limited != contenders-1 {
		t.Fatalf("released capacity outcomes: success=%d limited=%d", successes, limited)
	}
	var active int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sandboxes WHERE observed_state <> ?", domain.StateTerminated).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != limit {
		t.Fatalf("active quota=%d want %d", active, limit)
	}
}
