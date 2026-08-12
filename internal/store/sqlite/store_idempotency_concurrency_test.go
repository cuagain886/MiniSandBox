package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"minisandbox/internal/domain"
)

// TestCreateIdempotentConcurrentSameIdentityReturnsOneSandbox 验证几十个同 key/hash 请求
// 只能提交一个 sandbox，其余请求逐字节重放同一响应和同一资源 ID。
func TestCreateIdempotentConcurrentSameIdentityReturnsOneSandbox(t *testing.T) {
	store := migrateTestStore(t)
	const requests = 48
	start := make(chan struct{})
	ids := make([]string, requests)
	replayed := make([]bool, requests)
	errs := make([]error, requests)
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := idempotentCreateRequest(fmt.Sprintf("idem-concurrent-%02d", index), "shared-key")
			<-start
			result, err := store.CreateIdempotent(context.Background(), request)
			errs[index] = err
			ids[index] = result.Sandbox.ID
			replayed[index] = result.Replayed
		}(index)
	}
	close(start)
	wait.Wait()
	winner := ""
	firstResponses := 0
	for index := range requests {
		if errs[index] != nil {
			t.Fatalf("request %d: %v", index, errs[index])
		}
		if winner == "" {
			winner = ids[index]
		}
		if ids[index] != winner {
			t.Fatalf("request %d returned ID %q, winner %q", index, ids[index], winner)
		}
		if !replayed[index] {
			firstResponses++
		}
	}
	if firstResponses != 1 {
		t.Fatalf("non-replayed responses=%d want 1", firstResponses)
	}
	assertIdempotentCounts(t, store, 1, 1)
}

// TestCreateIdempotentConcurrentDifferentHashesSelectsOneIdentity 验证相同 key 的不同 hash
// 同刻竞争时仅有一种身份获胜；败方统一返回冲突且不会留下候选 sandbox。
func TestCreateIdempotentConcurrentDifferentHashesSelectsOneIdentity(t *testing.T) {
	store := migrateTestStore(t)
	const requests = 40
	start := make(chan struct{})
	hashes := make([]string, requests)
	errs := make([]error, requests)
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := idempotentCreateRequest(fmt.Sprintf("idem-conflict-%02d", index), "conflicting-key")
			if index%2 == 1 {
				request.RequestHash = strings.Repeat("b", 64)
			}
			hashes[index] = request.RequestHash
			<-start
			_, errs[index] = store.CreateIdempotent(context.Background(), request)
		}(index)
	}
	close(start)
	wait.Wait()
	winnerHash := ""
	successes, conflicts := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			successes++
			if winnerHash == "" {
				winnerHash = hashes[index]
			}
			if hashes[index] != winnerHash {
				t.Fatalf("both request hashes succeeded: winner=%q other=%q", winnerHash, hashes[index])
			}
		case errors.Is(err, domain.ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("request %d: %v", index, err)
		}
	}
	if successes != requests/2 || conflicts != requests/2 {
		t.Fatalf("outcomes: success=%d conflict=%d", successes, conflicts)
	}
	assertIdempotentCounts(t, store, 1, 1)
}
