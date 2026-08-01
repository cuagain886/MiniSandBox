//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/domain"
	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/pkg/protocol"
)

// TestSandboxdRestartRecoversRunningSandbox 验证控制面重启复用原 container。
func TestSandboxdRestartRecoversRunningSandbox(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	first := harness.startSandboxd(t)

	sandbox := createSandbox(t, first.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(
		t,
		first.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)
	firstContainerID := harness.runningContainerID(t, sandbox.ID)
	first.stop(t)

	second := harness.startSandboxd(t)
	waitSandboxState(
		t,
		second.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)
	secondContainerID := harness.runningContainerID(t, sandbox.ID)
	if secondContainerID != firstContainerID {
		t.Fatalf(
			"container identity changed across restart: first=%s second=%s",
			firstContainerID,
			secondContainerID,
		)
	}
	containers, volumes := harness.sandboxResourceCounts(t, sandbox.ID)
	if containers != 1 || volumes != 1 {
		t.Fatalf(
			"restart created duplicate resources: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}

	store, err := sqlitestore.Open(
		filepath.Join(harness.dataDirectory, "sandboxd.db"),
	)
	if err != nil {
		t.Fatalf("open recovered SQLite store: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close recovered SQLite store: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := store.Get(ctx, sandbox.ID)
	if err != nil {
		t.Fatalf("read recovered sandbox record: %v", err)
	}
	if record.RuntimeID != firstContainerID ||
		record.DesiredState != domain.DesiredRunning ||
		record.ObservedState != domain.StateRunning {
		t.Fatalf(
			"recovered store identity/state: runtime=%q desired=%s observed=%s",
			record.RuntimeID,
			record.DesiredState,
			record.ObservedState,
		)
	}
}
