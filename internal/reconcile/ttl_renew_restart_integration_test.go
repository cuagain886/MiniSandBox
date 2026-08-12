package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
	sqlitestore "minisandbox/internal/store/sqlite"
)

// TestRenewedLeaseSurvivesStaleTimerHeapLossAndRestart 验证 Store 中的最新租约同时压过旧
// timer、旧 lease.json 和丢失的内存 heap；真正到达新 expiry 后仍由周期 scanner 收敛删除。
func TestRenewedLeaseSurvivesStaleTimerHeapLossAndRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sandboxd.db")
	first, err := sqlitestore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	firstOpen := true
	defer func() {
		if firstOpen {
			_ = first.Close()
		}
	}()
	if err := first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2027, 10, 11, 12, 13, 14, 0, time.UTC)
	oldExpiry := createdAt.Add(10 * time.Second)
	newExpiry := createdAt.Add(30 * time.Second)
	record := cleanupRecoveryRecord(createdAt)
	record.ID = "20010203-0405-4607-8809-0a0b0c0d0e0f"
	record.SpecHash = strings.Repeat("a", 64)
	record.DesiredState = domain.DesiredRunning
	record.ExpiresAt = &oldExpiry
	if err := first.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	stored, err := first.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 先写出旧投影，模拟续期提交前容器目录中的状态。
	runRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(runRoot, record.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := runtimeport.NewLeaseManifestWriter(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(runtimeport.LeaseManifest{
		SchemaVersion: runtimeport.LeaseManifestSchemaVersion, SandboxID: record.ID,
		SpecHash: record.SpecHash, ExpiresAt: oldExpiry, ProjectedStoreRevision: stored.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	renewed, err := first.Renew(ctx, storeport.RenewUpdate{
		ID: record.ID, ExpectedRevision: stored.Revision, Now: createdAt.Add(time.Second), ExpiresAt: newExpiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldEntry := TTLHeapEntry{SandboxID: record.ID, ExpectedExpiresAt: oldExpiry}
	heap := NewTTLHeap()
	heap.Upsert(oldEntry)
	clock := &restartClock{now: oldExpiry}
	expiration := NewTTLExpirationCoordinator(first, heap, clock, nil)
	if err := expiration.ExpireEntry(ctx, oldEntry); err != nil {
		t.Fatal(err)
	}
	afterOldTimer, err := first.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterOldTimer.DesiredState != domain.DesiredRunning || afterOldTimer.ExpiresAt == nil || !afterOldTimer.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("stale timer changed renewed lease: %#v", afterOldTimer)
	}
	assertTTLHeapPeek(t, heap, record.ID, newExpiry)

	// renew wake 所触发的普通 reconcile 刷新非权威 lease.json；投影 revision 必须来自同一 Store snapshot。
	runtime := newCleanupFaultRuntime(map[string]int{})
	reconciler, err := NewWithRetry(first, runtime, restartProbe{}, clock, restartRandom{}, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.SetLeaseProjector(writer)
	if err := reconciler.Reconcile(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(runRoot, record.ID, runtimeport.LeaseManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := runtimeport.DecodeLeaseManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.ExpiresAt.Equal(newExpiry) || manifest.ProjectedStoreRevision != renewed.Revision {
		t.Fatalf("renewed lease projection: %#v store=%#v", manifest, renewed)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstOpen = false
	second, err := sqlitestore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	// 新进程没有旧 heap；启动恢复必须只重建 Store 当前的 new expiry。
	recoveryClock := newManualClock(oldExpiry)
	scheduler := NewTTLScheduler(recoveryClock, nil)
	recoveryExpiration := NewTTLExpirationCoordinator(second, scheduler, recoveryClock, nil)
	recovery, err := NewTTLRecovery(second, scheduler, recoveryExpiration, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	entry, ok := scheduler.Peek()
	if !ok || entry.SandboxID != record.ID || !entry.ExpectedExpiresAt.Equal(newExpiry) {
		t.Fatalf("restart heap entry: %#v ok=%v", entry, ok)
	}

	// 再次丢弃 heap，证明 scanner 能从 SQLite 发现真实到期并走同一删除收敛路径。
	clock.now = newExpiry
	reconciler, err = NewWithRetry(second, runtime, restartProbe{}, clock, restartRandom{}, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	sweeper, err := NewCandidateSweeper(second, 10, 10, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := sweepAndReconcile(ctx, sweeper, reconciler, newExpiry); err != nil {
		t.Fatal(err)
	}
	terminated, err := second.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminated.DesiredState != domain.DesiredTerminated || terminated.ObservedState != domain.StateTerminated ||
		terminated.Reason != domain.SandboxReasonTerminated {
		t.Fatalf("actual expiry did not converge: %#v", terminated)
	}
}
