package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"minisandbox/internal/domain"
)

// TestCreateIdempotentRejectsHashConflict 验证冲突不回显或修改既有记录。
func TestCreateIdempotentRejectsHashConflict(t *testing.T) {
	store := migrateTestStore(t)
	first := idempotentCreateRequest("hash-first", "secret-conflict-key")
	created, err := store.CreateIdempotent(context.Background(), first)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	conflict := idempotentCreateRequest("hash-second", first.Key)
	conflict.RequestHash = strings.Repeat("b", 64)
	_, err = store.CreateIdempotent(context.Background(), conflict)
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("hash conflict: got %v, want ErrIdempotencyConflict", err)
	}
	if strings.Contains(err.Error(), first.Key) || strings.Contains(err.Error(), first.RequestHash) ||
		strings.Contains(err.Error(), conflict.RequestHash) {
		t.Fatal("conflict error leaked key or hash")
	}
	assertIdempotentCounts(t, store, 1, 1)
	replayed, err := store.CreateIdempotent(context.Background(), first)
	if err != nil || !replayed.Replayed || replayed.Sandbox.ID != created.Sandbox.ID ||
		string(replayed.Response.Body) != string(created.Response.Body) {
		t.Fatalf("original binding changed: %#v/%v", replayed, err)
	}
}

// TestCreateIdempotentConcurrentHashConflict 验证同 key 不同 hash 只有一个首次创建成功。
func TestCreateIdempotentConcurrentHashConflict(t *testing.T) {
	store := migrateTestStore(t)
	const count = 8
	start := make(chan struct{})
	errorsByIndex := make([]error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := idempotentCreateRequest(fmt.Sprintf("concurrent-%d", index), "shared-key")
			hash := sha256.Sum256([]byte(fmt.Sprintf("request-%d", index)))
			request.RequestHash = hex.EncodeToString(hash[:])
			<-start
			_, errorsByIndex[index] = store.CreateIdempotent(context.Background(), request)
		}(index)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range errorsByIndex {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != count-1 {
		t.Fatalf("concurrent outcomes: success=%d conflict=%d", successes, conflicts)
	}
	assertIdempotentCounts(t, store, 1, 1)
}
