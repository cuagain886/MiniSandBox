package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestDeleteExpiredIdempotencyRecordsHonorsTerminalRetention 验证活跃及宽限内记录永不误删。
func TestDeleteExpiredIdempotencyRecordsHonorsTerminalRetention(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	seedGCRecord(t, store, "gc-active", "active-key", domain.StateRunning, now.Add(-48*time.Hour))
	seedGCRecord(t, store, "gc-recent", "recent-key", domain.StateTerminated, now.Add(-23*time.Hour))
	seedGCRecord(t, store, "gc-expired", "expired-key", domain.StateTerminated, now.Add(-24*time.Hour))

	batch, err := store.DeleteExpiredIdempotencyRecords(context.Background(), storeport.IdempotencyGCQuery{
		Now: now, TerminalRetention: 24 * time.Hour, Limit: 10,
	})
	if err != nil || batch.Deleted != 1 || batch.LastKey != "expired-key" {
		t.Fatalf("GC batch: %#v/%v", batch, err)
	}
	assertIdempotentCounts(t, store, 3, 2)
	if _, err := store.Get(context.Background(), "gc-expired"); err != nil {
		t.Fatalf("GC deleted sandbox record: %v", err)
	}
	for _, request := range []storeport.IdempotentCreateRequest{
		idempotentCreateRequest("gc-active-retry", "active-key"),
		idempotentCreateRequest("gc-recent-retry", "recent-key"),
	} {
		result, err := store.CreateIdempotent(context.Background(), request)
		if err != nil || !result.Replayed {
			t.Fatalf("retained replay: %#v/%v", result, err)
		}
	}
	reused := idempotentCreateRequest("gc-expired-reused", "expired-key")
	if result, err := store.CreateIdempotent(context.Background(), reused); err != nil || result.Replayed {
		t.Fatalf("expired key reuse: %#v/%v", result, err)
	}
}

// TestDeleteExpiredIdempotencyRecordsPaginatesByStableKey 验证分页无 OFFSET 漂移。
func TestDeleteExpiredIdempotencyRecordsPaginatesByStableKey(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		seedGCRecord(t, store, fmt.Sprintf("gc-page-%d", index), fmt.Sprintf("key-%d", index), domain.StateTerminated, now.Add(-25*time.Hour))
	}
	query := storeport.IdempotencyGCQuery{Now: now, TerminalRetention: 24 * time.Hour, Limit: 2}
	deleted := 0
	for {
		batch, err := store.DeleteExpiredIdempotencyRecords(context.Background(), query)
		if err != nil {
			t.Fatalf("GC page: %v", err)
		}
		deleted += batch.Deleted
		if batch.Deleted < query.Limit {
			break
		}
		query.AfterScopeID, query.AfterKey = batch.LastScopeID, batch.LastKey
	}
	if deleted != 5 {
		t.Fatalf("deleted=%d, want 5", deleted)
	}
	assertIdempotentCounts(t, store, 5, 0)
}

// TestDeleteExpiredIdempotencyRecordsRollsBackFailure 验证删除错误不留下半批结果。
func TestDeleteExpiredIdempotencyRecordsRollsBackFailure(t *testing.T) {
	store := migrateTestStore(t)
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	seedGCRecord(t, store, "gc-failure", "failure-key", domain.StateTerminated, now.Add(-25*time.Hour))
	if _, err := store.db.Exec(`CREATE TRIGGER reject_idempotency_delete
		BEFORE DELETE ON idempotency_records BEGIN SELECT RAISE(ABORT, 'injected GC failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	_, err := store.DeleteExpiredIdempotencyRecords(context.Background(), storeport.IdempotencyGCQuery{
		Now: now, TerminalRetention: 24 * time.Hour, Limit: 10,
	})
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unexpected GC error: %v", err)
	}
	assertIdempotentCounts(t, store, 1, 1)
}

func seedGCRecord(t *testing.T, store *Store, id, key string, state domain.SandboxState, transitionedAt time.Time) {
	t.Helper()
	request := idempotentCreateRequest(id, key)
	if _, err := store.CreateIdempotent(context.Background(), request); err != nil {
		t.Fatalf("seed GC record %s: %v", id, err)
	}
	if _, err := store.db.Exec(
		"UPDATE sandboxes SET observed_state = ?, last_transition_at = ? WHERE id = ?",
		state, transitionedAt.UTC().Format(time.RFC3339Nano), id,
	); err != nil {
		t.Fatalf("update GC sandbox %s: %v", id, err)
	}
}
