package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestIdempotencyIdentityPrecedesFullAdmission 验证满额时 replay/conflict 均先于 quota。
func TestIdempotencyIdentityPrecedesFullAdmission(t *testing.T) {
	store := migrateTestStore(t)
	first := idempotentCreateRequest("full-first", "full-key")
	first.MaxSandboxes = 1
	created, err := store.CreateIdempotent(context.Background(), first)
	if err != nil {
		t.Fatalf("fill capacity: %v", err)
	}

	replayRequest := idempotentCreateRequest("discarded-full-replay", first.Key)
	replayRequest.RequestHash = first.RequestHash
	replayRequest.MaxSandboxes = 1
	replayed, err := store.CreateIdempotent(context.Background(), replayRequest)
	if err != nil || !replayed.Replayed || replayed.Sandbox.ID != created.Sandbox.ID {
		t.Fatalf("full replay: %#v/%v", replayed, err)
	}

	conflict := idempotentCreateRequest("discarded-full-conflict", first.Key)
	conflict.RequestHash = strings.Repeat("b", 64)
	conflict.MaxSandboxes = 1
	if _, err := store.CreateIdempotent(context.Background(), conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("full conflict: got %v, want idempotency conflict", err)
	}

	newKey := idempotentCreateRequest("full-new-key", "new-key")
	newKey.MaxSandboxes = 1
	if _, err := store.CreateIdempotent(context.Background(), newKey); !errors.Is(err, domain.ErrSandboxLimitReached) {
		t.Fatalf("full new key: got %v, want sandbox limit", err)
	}
	if _, err := store.CreateNonIdempotent(context.Background(), storeport.NonIdempotentCreateRequest{
		Sandbox: nonIdempotentSandbox("full-no-key"), MaxSandboxes: 1,
	}); !errors.Is(err, domain.ErrSandboxLimitReached) {
		t.Fatalf("full no-key: got %v, want sandbox limit", err)
	}
	assertIdempotentCounts(t, store, 1, 1)
}
