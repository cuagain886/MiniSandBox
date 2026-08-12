package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// TestImportRecoveredPersistsAtomicOriginAndLease 验证可信 orphan 的完整记录一次提交。
func TestImportRecoveredPersistsAtomicOriginAndLease(t *testing.T) {
	store := migrateTestStore(t)
	sandbox := recoveredImportSandbox()
	got, err := store.ImportRecovered(context.Background(), storeport.RecoveredImportRequest{Sandbox: sandbox})
	if err != nil || got.Revision != 1 || got.Origin != domain.SandboxOriginRecoveredOrphan ||
		got.DesiredState != domain.DesiredRunning || got.ObservedState != domain.StateCreating || got.RuntimeID != sandbox.RuntimeID ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(*sandbox.ExpiresAt) {
		t.Fatalf("import: %#v/%v", got, err)
	}
}

// TestImportRecoveredConcurrentIDIsUnique 验证并发启动只有一个事务导入相同 sandbox ID。
func TestImportRecoveredConcurrentIDIsUnique(t *testing.T) {
	store := migrateTestStore(t)
	request := storeport.RecoveredImportRequest{Sandbox: recoveredImportSandbox()}
	var wait sync.WaitGroup
	errorsOut := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.ImportRecovered(context.Background(), request)
			errorsOut <- err
		}()
	}
	wait.Wait()
	close(errorsOut)
	successes, conflicts := 0, 0
	for err := range errorsOut {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected import error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

// TestImportRecoveredRejectsUntrustedShapeAndExistingAPIRecord 验证导入端口不接受普通来源且不覆盖已有记录。
func TestImportRecoveredRejectsUntrustedShapeAndExistingAPIRecord(t *testing.T) {
	store := migrateTestStore(t)
	invalid := recoveredImportSandbox()
	invalid.Origin = domain.SandboxOriginAPI
	if _, err := store.ImportRecovered(context.Background(), storeport.RecoveredImportRequest{Sandbox: invalid}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid shape: %v", err)
	}
	existing := createTestSandbox()
	existing.ID = recoveredImportSandbox().ID
	if err := store.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportRecovered(context.Background(), storeport.RecoveredImportRequest{Sandbox: recoveredImportSandbox()}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("existing record overwritten: %v", err)
	}
}

func recoveredImportSandbox() domain.Sandbox {
	now := time.Unix(100, 0).UTC()
	expires := now.Add(time.Hour)
	spec := domain.SandboxSpec{
		Image: "busybox:1.36", Resources: domain.ResourceLimits{CPUQuotaMillis: 500, MemoryMiB: 256, PIDs: 64},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath}, Platform: domain.Platform{OS: "linux", Arch: "amd64"},
	}
	message, _ := domain.SandboxReasonPublicMessage(domain.SandboxReasonOrphanImported)
	return domain.Sandbox{
		ID: "00010203-0405-4607-8809-0a0b0c0d0e0f", Spec: spec, SpecHash: spec.Hash(),
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateCreating,
		Reason: domain.SandboxReasonOrphanImported, Message: message, RuntimeID: "main",
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, LastTransitionAt: now, ExpiresAt: &expires,
		Origin: domain.SandboxOriginRecoveredOrphan,
	}
}

var _ storeport.RecoveredImporter = (*Store)(nil)
