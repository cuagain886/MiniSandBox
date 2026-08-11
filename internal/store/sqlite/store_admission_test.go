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

// TestSandboxAdmissionLimitBoundary 验证 limit-1 可创建、达到 limit 后拒绝两种分支。
func TestSandboxAdmissionLimitBoundary(t *testing.T) {
	store := migrateTestStore(t)
	for _, id := range []string{"limit-one", "limit-two"} {
		_, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
			Sandbox: nonIdempotentSandbox(id), MaxSandboxes: 2,
		})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if _, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
		Sandbox: nonIdempotentSandbox("limit-three"), MaxSandboxes: 2,
	}); !errors.Is(err, domain.ErrSandboxLimitReached) {
		t.Fatalf("no-key limit: got %v", err)
	}
	keyed := idempotentCreateRequest("limit-keyed", "limit-key")
	keyed.MaxSandboxes = 2
	if _, err := store.CreateIdempotent(context.Background(), keyed); !errors.Is(err, domain.ErrSandboxLimitReached) {
		t.Fatalf("keyed limit: got %v", err)
	}
	assertIdempotentCounts(t, store, 2, 0)
}

// TestSandboxAdmissionCountsEveryNonTerminalState 验证 Pending/Stopping/Failed 计数且 Terminated 释放容量。
func TestSandboxAdmissionCountsEveryNonTerminalState(t *testing.T) {
	store := migrateTestStore(t)
	states := []struct {
		id       string
		desired  domain.DesiredState
		observed domain.SandboxState
	}{
		{"count-pending", domain.DesiredRunning, domain.StatePending},
		{"count-stopping", domain.DesiredTerminated, domain.StateStopping},
		{"count-failed", domain.DesiredRunning, domain.StateFailed},
		{"ignore-terminated", domain.DesiredTerminated, domain.StateTerminated},
	}
	for _, state := range states {
		sandbox := createTestSandbox()
		sandbox.ID, sandbox.DesiredState, sandbox.ObservedState = state.id, state.desired, state.observed
		if err := store.Create(context.Background(), sandbox); err != nil {
			t.Fatalf("seed %s: %v", state.id, err)
		}
	}
	request := storeport.NonIdempotentCreateRequest{Sandbox: nonIdempotentSandbox("blocked-by-three"), MaxSandboxes: 3}
	if _, err := store.CreateNonIdempotent(context.Background(), request); !errors.Is(err, domain.ErrSandboxLimitReached) {
		t.Fatalf("three active states: got %v", err)
	}
	if _, err := store.db.Exec("UPDATE sandboxes SET observed_state = ? WHERE id = ?", domain.StateTerminated, "count-failed"); err != nil {
		t.Fatalf("terminate active record: %v", err)
	}
	if _, err := store.CreateNonIdempotent(context.Background(), request); err != nil {
		t.Fatalf("capacity was not released: %v", err)
	}
}

// TestSandboxAdmissionConcurrentLimit 验证 BEGIN IMMEDIATE 下 limit+N 并发不超额。
func TestSandboxAdmissionConcurrentLimit(t *testing.T) {
	store := migrateTestStore(t)
	const total, limit = 12, 3
	start := make(chan struct{})
	results := make([]error, total)
	var wait sync.WaitGroup
	for index := 0; index < total; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, results[index] = store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
				Sandbox: nonIdempotentSandbox(fmt.Sprintf("admission-%02d", index)), MaxSandboxes: limit,
			})
		}(index)
	}
	close(start)
	wait.Wait()
	successes, limited := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrSandboxLimitReached):
			limited++
		default:
			t.Fatalf("unexpected admission result: %v", err)
		}
	}
	if successes != limit || limited != total-limit {
		t.Fatalf("admission outcomes: success=%d limited=%d", successes, limited)
	}
	assertIdempotentCounts(t, store, limit, 0)
}

// TestSandboxAdmissionRejectsInvalidLimit 验证非正上限不会退化为无限制。
func TestSandboxAdmissionRejectsInvalidLimit(t *testing.T) {
	store := migrateTestStore(t)
	for _, limit := range []int{0, -1} {
		_, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
			Sandbox: nonIdempotentSandbox("invalid-limit"), MaxSandboxes: limit,
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("limit %d: got %v, want ErrInvalid", limit, err)
		}
	}
	assertIdempotentCounts(t, store, 0, 0)
}
