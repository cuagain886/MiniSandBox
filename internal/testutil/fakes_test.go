package testutil

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

// TestFakeStoreRecordsCallsAndInjectsResults 验证 Store fake 不模拟数据库而是按配置返回。
func TestFakeStoreRecordsCallsAndInjectsResults(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("injected store error")
	sandbox := domain.Sandbox{ID: "sandbox-1", Revision: 3}
	fake := NewFakeStore()

	fake.SetCreateError(injected)
	if err := fake.Create(ctx, sandbox); !errors.Is(err, injected) {
		t.Fatalf("Create error: got %v, want injected", err)
	}
	if got := fake.CreateCalls(); !reflect.DeepEqual(got, []domain.Sandbox{sandbox}) {
		t.Fatalf("Create calls: %#v", got)
	}

	fake.SetGetResult(sandbox, injected)
	if got, err := fake.Get(ctx, sandbox.ID); got != sandbox || !errors.Is(err, injected) {
		t.Fatalf("Get result: got %#v/%v", got, err)
	}
	if got := fake.GetCalls(); !reflect.DeepEqual(got, []string{sandbox.ID}) {
		t.Fatalf("Get calls: %#v", got)
	}

	fake.SetUpdateDesiredResult(sandbox, injected)
	_, _ = fake.UpdateDesired(
		ctx,
		sandbox.ID,
		domain.DesiredTerminated,
		sandbox.Revision,
	)
	wantDesiredCall := DesiredUpdateCall{
		ID:               sandbox.ID,
		Desired:          domain.DesiredTerminated,
		ExpectedRevision: sandbox.Revision,
	}
	if got := fake.UpdateDesiredCalls(); !reflect.DeepEqual(
		got,
		[]DesiredUpdateCall{wantDesiredCall},
	) {
		t.Fatalf("UpdateDesired calls: %#v", got)
	}

	observedCall := storeport.ObservedUpdate{
		ID:               sandbox.ID,
		ExpectedRevision: sandbox.Revision,
		State:            domain.StateRunning,
	}
	fake.SetUpdateObservedResult(sandbox, injected)
	_, _ = fake.UpdateObserved(ctx, observedCall)
	if got := fake.UpdateObservedCalls(); !reflect.DeepEqual(
		got,
		[]storeport.ObservedUpdate{observedCall},
	) {
		t.Fatalf("UpdateObserved calls: %#v", got)
	}

	now := time.Now().UTC()
	renewCall := storeport.RenewUpdate{ID: sandbox.ID, ExpectedRevision: 1, Now: now, ExpiresAt: now.Add(time.Hour)}
	fake.SetRenewResult(sandbox, injected)
	_, _ = fake.Renew(ctx, renewCall)
	if got := fake.RenewCalls(); !reflect.DeepEqual(got, []storeport.RenewUpdate{renewCall}) {
		t.Fatalf("Renew calls: %#v", got)
	}
	expireCall := storeport.ExpireIntentUpdate{ID: sandbox.ID, ExpectedRevision: 1, ExpectedExpiresAt: now, Now: now}
	fake.SetExpireIntentResult(sandbox, injected)
	_, _ = fake.ExpireIntent(ctx, expireCall)
	if got := fake.ExpireIntentCalls(); !reflect.DeepEqual(got, []storeport.ExpireIntentUpdate{expireCall}) {
		t.Fatalf("ExpireIntent calls: %#v", got)
	}
	retryCall := storeport.RetryUpdate{ID: sandbox.ID, ExpectedRevision: 1, AttemptedAt: now, NextReconcileAt: now.Add(time.Second)}
	fake.SetScheduleRetryResult(sandbox, injected)
	_, _ = fake.ScheduleRetry(ctx, retryCall)
	if got := fake.ScheduleRetryCalls(); !reflect.DeepEqual(got, []storeport.RetryUpdate{retryCall}) {
		t.Fatalf("ScheduleRetry calls: %#v", got)
	}
	resetCall := storeport.RetryResetUpdate{Observed: observedCall, ReconciledAt: now}
	fake.SetResetRetryResult(sandbox, injected)
	_, _ = fake.ResetRetry(ctx, resetCall)
	if got := fake.ResetRetryCalls(); !reflect.DeepEqual(got, []storeport.RetryResetUpdate{resetCall}) {
		t.Fatalf("ResetRetry calls: %#v", got)
	}
	healthCall := storeport.HealthResultUpdate{ID: sandbox.ID, ExpectedRevision: 1, CheckedAt: now, Healthy: true}
	fake.SetHealthResult(sandbox, injected)
	_, _ = fake.RecordHealthResult(ctx, healthCall)
	if got := fake.HealthResultCalls(); !reflect.DeepEqual(got, []storeport.HealthResultUpdate{healthCall}) {
		t.Fatalf("RecordHealthResult calls: %#v", got)
	}

	fake.SetListReconcileCandidatesResult([]domain.Sandbox{sandbox}, injected)
	query := storeport.ReconcileCandidateQuery{
		Now:           time.Now(),
		RunningCutoff: time.Now(),
		Limit:         25,
	}
	listed, err := fake.ListReconcileCandidates(ctx, query)
	if !errors.Is(err, injected) || !reflect.DeepEqual(listed, []domain.Sandbox{sandbox}) {
		t.Fatalf("ListReconcileCandidates result: %#v/%v", listed, err)
	}
	listed[0].ID = "mutated"
	listedAgain, _ := fake.ListReconcileCandidates(ctx, query)
	if listedAgain[0].ID != sandbox.ID {
		t.Fatal("candidate result mutation changed configured fake result")
	}

	fake.SetListAllResult([]domain.Sandbox{sandbox}, injected)
	_, _ = fake.ListAll(ctx)
	if fake.ListAllCallCount() != 1 {
		t.Fatalf("ListAll calls: %d", fake.ListAllCallCount())
	}
}

// TestFakeRuntimeRecordsCallsAndInjectsResults 验证 Runtime fake 精确记录端口参数。
func TestFakeRuntimeRecordsCallsAndInjectsResults(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("injected runtime error")
	sandbox := domain.Sandbox{ID: "sandbox-1"}
	actual := runtimeport.ActualSandbox{
		ID:        sandbox.ID,
		RuntimeID: "container-1",
		State:     runtimeport.ActualRunning,
	}
	fake := NewFakeRuntime()

	fake.SetEnsureResult(actual, injected)
	if got, err := fake.Ensure(ctx, sandbox); got != actual || !errors.Is(err, injected) {
		t.Fatalf("Ensure result: got %#v/%v", got, err)
	}
	if got := fake.EnsureCalls(); !reflect.DeepEqual(got, []domain.Sandbox{sandbox}) {
		t.Fatalf("Ensure calls: %#v", got)
	}

	fake.SetInspectResult(actual, injected)
	_, _ = fake.Inspect(ctx, sandbox.ID)
	if got := fake.InspectCalls(); !reflect.DeepEqual(got, []string{sandbox.ID}) {
		t.Fatalf("Inspect calls: %#v", got)
	}

	fake.SetDeleteError(injected)
	if err := fake.Delete(ctx, sandbox.ID); !errors.Is(err, injected) {
		t.Fatalf("Delete error: got %v, want injected", err)
	}
	if got := fake.DeleteCalls(); !reflect.DeepEqual(got, []string{sandbox.ID}) {
		t.Fatalf("Delete calls: %#v", got)
	}

	fake.SetListManagedResult([]runtimeport.ActualSandbox{actual}, injected)
	listed, err := fake.ListManaged(ctx)
	if !errors.Is(err, injected) ||
		!reflect.DeepEqual(listed, []runtimeport.ActualSandbox{actual}) {
		t.Fatalf("ListManaged result: %#v/%v", listed, err)
	}
	listed[0].ID = "mutated"
	listedAgain, _ := fake.ListManaged(ctx)
	if listedAgain[0].ID != actual.ID {
		t.Fatal("managed result mutation changed configured fake result")
	}
	if fake.ListManagedCallCount() != 2 {
		t.Fatalf("ListManaged calls: %d", fake.ListManagedCallCount())
	}
}

// TestFakesConcurrentAccess 验证调用记录和结果注入在并发访问下保持一致。
func TestFakesConcurrentAccess(t *testing.T) {
	const goroutines = 100
	ctx := context.Background()
	storeFake := NewFakeStore()
	runtimeFake := NewFakeRuntime()
	wakerFake := NewFakeWaker()

	var wait sync.WaitGroup
	wait.Add(goroutines)
	for index := 0; index < goroutines; index++ {
		go func() {
			defer wait.Done()
			sandbox := domain.Sandbox{ID: "sandbox"}
			_ = storeFake.Create(ctx, sandbox)
			_, _ = storeFake.Get(ctx, sandbox.ID)
			_, _ = storeFake.UpdateDesired(
				ctx,
				sandbox.ID,
				domain.DesiredTerminated,
				1,
			)
			_, _ = storeFake.UpdateObserved(ctx, storeport.ObservedUpdate{
				ID:    sandbox.ID,
				State: domain.StateRunning,
			})
			now := time.Now()
			_, _ = storeFake.Renew(ctx, storeport.RenewUpdate{ID: sandbox.ID, Now: now, ExpiresAt: now.Add(time.Hour)})
			_, _ = storeFake.ExpireIntent(ctx, storeport.ExpireIntentUpdate{ID: sandbox.ID, ExpectedExpiresAt: now, Now: now})
			_, _ = storeFake.ScheduleRetry(ctx, storeport.RetryUpdate{ID: sandbox.ID, AttemptedAt: now, NextReconcileAt: now.Add(time.Second)})
			_, _ = storeFake.ResetRetry(ctx, storeport.RetryResetUpdate{Observed: storeport.ObservedUpdate{ID: sandbox.ID}, ReconciledAt: now})
			_, _ = storeFake.RecordHealthResult(ctx, storeport.HealthResultUpdate{ID: sandbox.ID, CheckedAt: now})
			_, _ = storeFake.ListReconcileCandidates(
				ctx,
				storeport.ReconcileCandidateQuery{
					Now:           time.Now(),
					RunningCutoff: time.Now(),
					Limit:         10,
				},
			)
			_, _ = storeFake.ListAll(ctx)

			_, _ = runtimeFake.Ensure(ctx, sandbox)
			_, _ = runtimeFake.Inspect(ctx, sandbox.ID)
			_ = runtimeFake.Delete(ctx, sandbox.ID)
			_, _ = runtimeFake.ListManaged(ctx)
			wakerFake.Wake(sandbox.ID)
		}()
	}
	wait.Wait()

	if got := len(storeFake.CreateCalls()); got != goroutines {
		t.Fatalf("concurrent Create calls: got %d, want %d", got, goroutines)
	}
	if got := len(storeFake.UpdateObservedCalls()); got != goroutines {
		t.Fatalf("concurrent UpdateObserved calls: got %d, want %d", got, goroutines)
	}
	if got := storeFake.ListAllCallCount(); got != goroutines {
		t.Fatalf("concurrent ListAll calls: got %d, want %d", got, goroutines)
	}
	if got := len(runtimeFake.EnsureCalls()); got != goroutines {
		t.Fatalf("concurrent Ensure calls: got %d, want %d", got, goroutines)
	}
	if got := runtimeFake.ListManagedCallCount(); got != goroutines {
		t.Fatalf("concurrent ListManaged calls: got %d, want %d", got, goroutines)
	}
	if got := len(wakerFake.WakeCalls()); got != goroutines {
		t.Fatalf("concurrent Wake calls: got %d, want %d", got, goroutines)
	}
}
