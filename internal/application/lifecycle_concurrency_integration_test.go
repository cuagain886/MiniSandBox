package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	sqlitestore "minisandbox/internal/store/sqlite"
)

// TestRenewDeleteRaceNeverRegressesExpiryOrResurrects 验证真实 SQLite CAS 下 renew/delete
// 同刻竞争时删除意图最终占优，租约不会倒退，且后续 renew 不能复活终止中的记录。
func TestRenewDeleteRaceNeverRegressesExpiryOrResurrects(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sandboxd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 11, 12, 13, 14, 15, 0, time.UTC)
	waker := &renewRecordingWaker{}
	service := &SandboxService{
		store: database, clock: &recordingClock{now: now}, waker: waker,
		createPolicy: CreatePolicy{MinimumTTL: time.Second, MaximumTTL: 24 * time.Hour},
	}

	const rounds = 32
	for round := 0; round < rounds; round++ {
		id := fmt.Sprintf("renew-delete-race-%02d", round)
		oldExpiry := now.Add(time.Hour)
		newExpiry := now.Add(2 * time.Hour)
		record := concurrencySandbox(id, now, oldExpiry)
		if err := database.Create(ctx, record); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		var renewErr, deleteErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, renewErr = service.Renew(ctx, RenewSandbox{SandboxID: id, ExpiresAt: newExpiry})
		}()
		go func() {
			defer wait.Done()
			<-start
			_, deleteErr = service.Delete(ctx, DeleteSandbox{SandboxID: id})
		}()
		close(start)
		wait.Wait()
		if deleteErr != nil {
			t.Fatalf("round %d delete: %v", round, deleteErr)
		}
		if renewErr != nil && !errors.Is(renewErr, domain.ErrSandboxExpiring) {
			t.Fatalf("round %d renew: %v", round, renewErr)
		}
		stored, err := database.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if stored.DesiredState != domain.DesiredTerminated || stored.ExpiresAt == nil ||
			(stored.ExpiresAt.Before(oldExpiry)) {
			t.Fatalf("round %d final record: %#v", round, stored)
		}
		if _, err := service.Renew(ctx, RenewSandbox{SandboxID: id, ExpiresAt: newExpiry.Add(time.Hour)}); !errors.Is(err, domain.ErrSandboxExpiring) {
			t.Fatalf("round %d post-delete renew: %v", round, err)
		}
	}
}

// TestConcurrentRenewKeepsMaximumExpiry 验证多个续期竞争者只允许 expiry 单调前进；较晚的
// 已提交值不会被较短请求覆盖，同值重放也不额外写入。
func TestConcurrentRenewKeepsMaximumExpiry(t *testing.T) {
	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "sandboxd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 11, 12, 13, 14, 15, 0, time.UTC)
	oldExpiry := now.Add(time.Hour)
	const contenders = 2
	record := concurrencySandbox("concurrent-renew", now, oldExpiry)
	if err := database.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	service := &SandboxService{
		store: database, clock: &recordingClock{now: now}, waker: &renewRecordingWaker{},
		createPolicy: CreatePolicy{MinimumTTL: time.Second, MaximumTTL: 24 * time.Hour},
	}
	start := make(chan struct{})
	errs := make([]error, contenders)
	var wait sync.WaitGroup
	for index := range contenders {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			expiresAt := oldExpiry.Add(time.Duration(index+1) * time.Minute)
			_, errs[index] = service.Renew(ctx, RenewSandbox{SandboxID: record.ID, ExpiresAt: expiresAt})
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if err != nil && !errors.Is(err, domain.ErrLeaseConflict) {
			t.Fatalf("renew %d: %v", index, err)
		}
	}
	stored, err := database.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := oldExpiry.Add(contenders * time.Minute)
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(want) {
		t.Fatalf("maximum expiry lost: got=%v want=%v record=%#v", stored.ExpiresAt, want, stored)
	}
}

func concurrencySandbox(id string, now, expiresAt time.Time) domain.Sandbox {
	spec := domain.SandboxSpec{
		Image:     "example.invalid/image:fixed",
		Resources: domain.ResourceLimits{CPUQuotaMillis: 100, MemoryMiB: 64, PIDs: 16},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath},
		Platform:  domain.Platform{OS: "linux", Arch: "amd64"},
	}
	return domain.Sandbox{
		ID: id, Spec: spec, DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning,
		Reason: domain.SandboxReasonRunning, Message: "running", RuntimeID: "runtime-" + id,
		SpecHash: spec.Hash(), Revision: 1, CreatedAt: now, UpdatedAt: now, LastTransitionAt: now,
		ExpiresAt: &expiresAt, Origin: domain.SandboxOriginAPI,
	}
}
