package sqlite

import (
	"context"
	"testing"

	"minisandbox/internal/domain"
)

// TestListActiveLeasesKeyset 验证只返回 active lease 且 ID 分页稳定。
func TestListActiveLeasesKeyset(t *testing.T) {
	store := migrateTestStore(t)
	for _, item := range []struct {
		id      string
		desired domain.DesiredState
		state   domain.SandboxState
	}{
		{"lease-a", domain.DesiredRunning, domain.StatePending},
		{"lease-b", domain.DesiredRunning, domain.StateRunning},
		{"lease-c", domain.DesiredTerminated, domain.StateStopping},
		{"lease-d", domain.DesiredRunning, domain.StateTerminated},
	} {
		createReliabilitySandbox(t, store, item.id, item.desired, item.state)
	}
	first, err := store.ListActiveLeases(context.Background(), "", 1)
	if err != nil || len(first) != 1 || first[0].ID != "lease-a" {
		t.Fatalf("first page: %#v/%v", first, err)
	}
	second, err := store.ListActiveLeases(context.Background(), first[0].ID, 2)
	if err != nil || len(second) != 1 || second[0].ID != "lease-b" {
		t.Fatalf("second page: %#v/%v", second, err)
	}
}
