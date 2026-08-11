package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestCreateIdempotentReplaysAcrossSandboxStates 验证当前状态变化不改写首次响应。
func TestCreateIdempotentReplaysAcrossSandboxStates(t *testing.T) {
	for _, state := range []domain.SandboxState{
		domain.StatePending,
		domain.StateRunning,
		domain.StateTerminated,
	} {
		t.Run(string(state), func(t *testing.T) {
			store := migrateTestStore(t)
			request := idempotentCreateRequest("first-sandbox", "replay-"+string(state))
			first, err := store.CreateIdempotent(context.Background(), request)
			if err != nil {
				t.Fatalf("first create: %v", err)
			}
			desired := domain.DesiredRunning
			if state == domain.StateTerminated {
				desired = domain.DesiredTerminated
			}
			if _, err := store.db.Exec(
				"UPDATE sandboxes SET desired_state = ?, observed_state = ?, reason = ?, revision = revision + 1 WHERE id = ?",
				desired, state, "STATE_CHANGED_AFTER_CREATE", request.Sandbox.ID,
			); err != nil {
				t.Fatalf("change sandbox state: %v", err)
			}
			replayRequest := idempotentCreateRequest("discarded-candidate", request.Key)
			replayRequest.RequestHash = request.RequestHash
			replayed, err := store.CreateIdempotent(context.Background(), replayRequest)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if !replayed.Replayed || replayed.Sandbox.ID != request.Sandbox.ID ||
				replayed.Response.StatusCode != first.Response.StatusCode ||
				replayed.Response.Location != first.Response.Location ||
				string(replayed.Response.Body) != string(first.Response.Body) ||
				!replayed.Response.CreatedAt.Equal(first.Response.CreatedAt) {
				t.Fatalf("replay drift:\nfirst=%#v\nreplay=%#v", first, replayed)
			}
			assertIdempotentCounts(t, store, 1, 1)
		})
	}
}

// TestCreateIdempotentReplayAfterReopen 验证重启后仍返回首次 bytes 和 sandbox ID。
func TestCreateIdempotentReplayAfterReopen(t *testing.T) {
	path := testDatabasePath(t)
	firstStore, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := firstStore.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	request := idempotentCreateRequest("replay-reopen", "replay-reopen-key")
	first, err := firstStore.CreateIdempotent(context.Background(), request)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	secondStore, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer secondStore.Close()
	replay, err := secondStore.CreateIdempotent(context.Background(), request)
	if err != nil {
		t.Fatalf("replay after reopen: %v", err)
	}
	if !replay.Replayed || replay.Sandbox.ID != first.Sandbox.ID || string(replay.Response.Body) != string(first.Response.Body) {
		t.Fatalf("reopen replay: %#v", replay)
	}
	assertIdempotentCounts(t, secondStore, 1, 1)
}

// TestCreateIdempotentReplayRejectsCorruptResponse 验证重放前执行 byte limit 和 schema v1 校验。
func TestCreateIdempotentReplayRejectsCorruptResponse(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body []byte
	}{
		{"missing schema fields", []byte(`{"id":"corrupt-response"}`)},
		{"unknown schema field", []byte(`{"id":"corrupt-response","state":"Pending","reason":"CREATE_ACCEPTED","message":"ok","image":"alpine","expires_at":"2027-01-01T01:00:00Z","created_at":"2027-01-01T00:00:00Z","updated_at":"2027-01-01T00:00:00Z","schema_version":2}`)},
		{"oversized", []byte(`{"payload":"` + strings.Repeat("x", maxIdempotencyResponseBytes) + `"}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := migrateTestStore(t)
			request := idempotentCreateRequest("corrupt-response", "corrupt-key")
			if _, err := store.CreateIdempotent(context.Background(), request); err != nil {
				t.Fatalf("first create: %v", err)
			}
			if _, err := store.db.Exec("PRAGMA ignore_check_constraints = ON"); err != nil {
				t.Fatalf("disable checks for corruption fixture: %v", err)
			}
			if _, err := store.db.Exec(
				"UPDATE idempotency_records SET response_json = ? WHERE scope_id = ? AND idempotency_key = ?",
				testCase.body, request.ScopeID, request.Key,
			); err != nil {
				t.Fatalf("inject corrupt response: %v", err)
			}
			_, err := store.CreateIdempotent(context.Background(), request)
			if !errors.Is(err, storeport.ErrCorrupt) {
				t.Fatalf("corrupt replay: got %v, want ErrCorrupt", err)
			}
			assertIdempotentCounts(t, store, 1, 1)
		})
	}
}
