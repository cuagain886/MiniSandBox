package reconcile

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	sqlitestore "minisandbox/internal/store/sqlite"
)

// TestTrustedOrphanAndAnomalyPolicyWithSQLite 验证完整可信、续期、过期和各类歧义
// bundle 在同一次真实 SQLite recovery 中严格分流，重复扫描不重复导入且歧义资源不被改写。
func TestTrustedOrphanAndAnomalyPolicyWithSQLite(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sandboxd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2028, 1, 2, 3, 4, 5, 0, time.UTC)
	wakes := map[string]int{}
	executor, err := NewInventoryRecoveryExecutor(database, database, &restartClock{now: now},
		func(id string) bool { wakes[id]++; return true }, true, DriftExpectation{})
	if err != nil {
		t.Fatal(err)
	}

	future := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e01", now)
	renewed := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e02", now)
	oldCreation := now.Add(-time.Minute)
	renewed.Main.CreationExpiresAt = &oldCreation
	renewed.Directory.Manifest.ExpiresAt = now.Add(2 * time.Hour)
	expired := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e03", now)
	expired.Directory.Manifest.ExpiresAt = now

	v1 := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e04", now)
	v1.Main.SchemaVersion, v1.Directory.Manifest = 1, nil
	unknownSchema := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e05", now)
	unknownSchema.Anomalies = []ActualAnomaly{{Code: ActualAnomalyResourceDamaged, Resource: "main", Detail: runtimeport.DiscoverySchemaUnsupported}}
	hashMismatch := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e06", now)
	hashMismatch.Workspace.SpecHash = "mismatch"
	hashMismatch.Anomalies = []ActualAnomaly{{Code: ActualAnomalySpecHashConflict}}
	partialMain := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e07", now)
	partialMain.Workspace = nil
	onlyVolume := ActualResourceSnapshot{SandboxID: "30010203-0405-4607-8809-0a0b0c0d0e08", Workspace: future.Workspace}
	symlinkDirectory := ActualResourceSnapshot{SandboxID: "30010203-0405-4607-8809-0a0b0c0d0e09",
		Anomalies: []ActualAnomaly{{Code: ActualAnomalyResourceDamaged, Resource: "directory", Detail: runtimeport.DiscoveryDirectoryUnsafe}}}
	mainSidecarPartial := orphanAcceptanceSnapshot(t, "30010203-0405-4607-8809-0a0b0c0d0e0a", now)
	mainSidecarPartial.Main.NetworkMode = "container"
	mainSidecarPartial.Anomalies = []ActualAnomaly{{Code: ActualAnomalyEgressMissing}}
	inventory := ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{
		future, renewed, expired, v1, unknownSchema, hashMismatch, partialMain, onlyVolume, symlinkDirectory, mainSidecarPartial,
	}}

	started := now
	if err := executor.Recover(ctx, inventory, started); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		id      string
		desired domain.DesiredState
	}{
		{future.SandboxID, domain.DesiredRunning}, {renewed.SandboxID, domain.DesiredRunning},
		{expired.SandboxID, domain.DesiredTerminated},
	} {
		record, err := database.Get(ctx, candidate.id)
		if err != nil || record.Origin != domain.SandboxOriginRecoveredOrphan || record.DesiredState != candidate.desired || wakes[candidate.id] != 1 {
			t.Fatalf("trusted orphan %s: %#v wakes=%d err=%v", candidate.id, record, wakes[candidate.id], err)
		}
	}
	if renewedRecord, _ := database.Get(ctx, renewed.SandboxID); renewedRecord.ExpiresAt == nil || !renewedRecord.ExpiresAt.Equal(renewed.Directory.Manifest.ExpiresAt) {
		t.Fatalf("renewed manifest was not authoritative: %#v", renewedRecord)
	}
	for _, ambiguous := range inventory.Sandboxes[3:] {
		if _, err := database.Get(ctx, ambiguous.SandboxID); err == nil {
			t.Fatalf("ambiguous orphan %s was imported", ambiguous.SandboxID)
		}
	}
	anomalies, err := database.ListActiveRuntimeAnomalies(ctx)
	if err != nil || len(anomalies) != len(inventory.Sandboxes)-3 {
		t.Fatalf("active anomalies=%d want=%d err=%v", len(anomalies), len(inventory.Sandboxes)-3, err)
	}

	// 重复 recovery 只增加安全 observation，不产生第二条 Store 记录。
	if err := executor.Recover(ctx, inventory, now); err == nil {
		// 已导入的三个 bundle 现在会按 Store/actual 关联规划；不安全 bundle 仍只记 anomaly。
	} else {
		t.Fatal(err)
	}
	records, err := database.ListAll(ctx)
	if err != nil || len(records) != 3 {
		t.Fatalf("repeated recovery records=%d err=%v", len(records), err)
	}

	// partial coverage 不得 resolve；完整且不再观察到资源的一轮才能关闭诊断。
	resolved, err := FinalizeAnomalyScan(ctx, database, ActualResourceInventory{},
		InventoryCoverage{ContainersComplete: true, VolumesComplete: false, FilesystemComplete: true}, now.Add(time.Second), now.Add(2*time.Second))
	if err != nil || resolved != 0 {
		t.Fatalf("partial inventory resolved anomalies: %d/%v", resolved, err)
	}
	resolved, err = FinalizeAnomalyScan(ctx, database, ActualResourceInventory{},
		InventoryCoverage{ContainersComplete: true, VolumesComplete: true, FilesystemComplete: true}, now.Add(time.Second), now.Add(2*time.Second))
	if err != nil || resolved != len(anomalies) {
		t.Fatalf("complete inventory resolved=%d want=%d err=%v", resolved, len(anomalies), err)
	}
}

func orphanAcceptanceSnapshot(t *testing.T, id string, now time.Time) ActualResourceSnapshot {
	t.Helper()
	_, actual, _ := driftFixture(false)
	actual.SandboxID = id
	actual.Main.SandboxID, actual.Main.ContainerID = id, "main-"+id
	actual.Main.CreatedAt = now.Add(-time.Hour)
	creationExpiry := now.Add(30 * time.Minute)
	actual.Main.CreationExpiresAt = &creationExpiry
	actual.Workspace.SandboxID, actual.Workspace.VolumeName = id, "workspace-"+id
	actual.Main.Workspace = actual.Workspace.VolumeName
	actual.Directory.SandboxID = id
	actual.Directory.Manifest.SandboxID = id
	actual.Directory.Manifest.ExpiresAt = now.Add(time.Hour)
	return actual
}
