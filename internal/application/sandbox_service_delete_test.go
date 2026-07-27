package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/internal/testutil"
)

// deleteRetryStore 为 CAS 冲突测试提供按调用顺序变化的 snapshot 和更新结果。
type deleteRetryStore struct {
	*testutil.FakeStore
	snapshots   []domain.Sandbox
	getCalls    int
	updateErrs  []error
	updateCalls []testutil.DesiredUpdateCall
	updated     domain.Sandbox
}

// Get 按顺序返回 snapshot，超过配置数量后重复最后一个。
func (s *deleteRetryStore) Get(
	context.Context,
	string,
) (domain.Sandbox, error) {
	index := s.getCalls
	s.getCalls++
	if index >= len(s.snapshots) {
		index = len(s.snapshots) - 1
	}
	return s.snapshots[index], nil
}

// UpdateDesired 按顺序返回冲突或最终成功结果并记录 revision。
func (s *deleteRetryStore) UpdateDesired(
	_ context.Context,
	id string,
	desired domain.DesiredState,
	revision uint64,
) (domain.Sandbox, error) {
	s.updateCalls = append(s.updateCalls, testutil.DesiredUpdateCall{
		ID:               id,
		Desired:          desired,
		ExpectedRevision: revision,
	})
	index := len(s.updateCalls) - 1
	if index < len(s.updateErrs) && s.updateErrs[index] != nil {
		return domain.Sandbox{}, s.updateErrs[index]
	}
	return s.updated, nil
}

var _ storeport.Store = (*deleteRetryStore)(nil)

// TestSandboxServiceDeleteSubmitsTermination 验证非终态记录通过一次 CAS 提交删除意图。
func TestSandboxServiceDeleteSubmitsTermination(t *testing.T) {
	for _, observed := range []domain.SandboxState{
		domain.StatePending,
		domain.StateRunning,
		domain.StateFailed,
	} {
		t.Run(string(observed), func(t *testing.T) {
			storeFake, idGenerator, clock, builder, waker :=
				newSandboxServiceTestDependencies()
			current := domain.Sandbox{
				ID:            "sandbox-1",
				DesiredState:  domain.DesiredRunning,
				ObservedState: observed,
				Revision:      7,
			}
			updated := current
			updated.DesiredState = domain.DesiredTerminated
			updated.Revision++
			storeFake.SetGetResult(current, nil)
			storeFake.SetUpdateDesiredResult(updated, nil)
			service := NewSandboxService(
				storeFake,
				idGenerator,
				clock,
				builder,
				waker,
			)

			got, err := service.Delete(
				context.Background(),
				DeleteSandbox{SandboxID: current.ID},
			)
			if err != nil {
				t.Fatalf("delete sandbox: %v", err)
			}
			if !reflect.DeepEqual(got, updated) {
				t.Fatalf("delete result: got %#v, want %#v", got, updated)
			}
			wantCall := testutil.DesiredUpdateCall{
				ID:               current.ID,
				Desired:          domain.DesiredTerminated,
				ExpectedRevision: current.Revision,
			}
			if calls := storeFake.UpdateDesiredCalls(); !reflect.DeepEqual(
				calls,
				[]testutil.DesiredUpdateCall{wantCall},
			) {
				t.Fatalf("UpdateDesired calls: %#v", calls)
			}
			if calls := waker.WakeCalls(); !reflect.DeepEqual(
				calls,
				[]string{current.ID},
			) {
				t.Fatalf("Wake calls: %v", calls)
			}
		})
	}
}

// TestSandboxServiceDeleteAlreadyDesiredWakesAgain 验证 Stopping 等记录可显式重试收敛。
func TestSandboxServiceDeleteAlreadyDesiredWakesAgain(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	current := domain.Sandbox{
		ID:            "sandbox-1",
		DesiredState:  domain.DesiredTerminated,
		ObservedState: domain.StateStopping,
		Revision:      8,
	}
	storeFake.SetGetResult(current, nil)
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Delete(
		context.Background(),
		DeleteSandbox{SandboxID: current.ID},
	)
	if err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("repeat delete result: got %#v, want %#v", got, current)
	}
	if len(storeFake.UpdateDesiredCalls()) != 0 {
		t.Fatalf("repeat delete updated Store: %#v", storeFake.UpdateDesiredCalls())
	}
	if calls := waker.WakeCalls(); !reflect.DeepEqual(calls, []string{current.ID}) {
		t.Fatalf("repeat delete Wake calls: %v", calls)
	}
}

// TestSandboxServiceDeleteTerminatedIsNoOp 验证终态记录不更新也不重复 Wake。
func TestSandboxServiceDeleteTerminatedIsNoOp(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	current := domain.Sandbox{
		ID:            "sandbox-1",
		DesiredState:  domain.DesiredTerminated,
		ObservedState: domain.StateTerminated,
		Revision:      9,
	}
	storeFake.SetGetResult(current, nil)
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Delete(
		context.Background(),
		DeleteSandbox{SandboxID: current.ID},
	)
	if err != nil {
		t.Fatalf("delete terminated sandbox: %v", err)
	}
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("terminated result: got %#v, want %#v", got, current)
	}
	if len(storeFake.UpdateDesiredCalls()) != 0 || len(waker.WakeCalls()) != 0 {
		t.Fatalf(
			"terminated delete caused side effects: update=%v wake=%v",
			storeFake.UpdateDesiredCalls(),
			waker.WakeCalls(),
		)
	}
}

// TestSandboxServiceDeleteRetriesOneCASConflict 验证冲突后只重读一次并使用新 revision。
func TestSandboxServiceDeleteRetriesOneCASConflict(t *testing.T) {
	first := domain.Sandbox{
		ID:            "sandbox-1",
		DesiredState:  domain.DesiredRunning,
		ObservedState: domain.StateRunning,
		Revision:      1,
	}
	latest := first
	latest.Revision = 2
	updated := latest
	updated.DesiredState = domain.DesiredTerminated
	updated.Revision = 3
	storeFake := &deleteRetryStore{
		FakeStore:  testutil.NewFakeStore(),
		snapshots:  []domain.Sandbox{first, latest},
		updateErrs: []error{domain.ErrConflict, nil},
		updated:    updated,
	}
	_, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Delete(
		context.Background(),
		DeleteSandbox{SandboxID: first.ID},
	)
	if err != nil {
		t.Fatalf("delete after conflict: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Fatalf("retry result: got %#v, want %#v", got, updated)
	}
	if storeFake.getCalls != 2 {
		t.Fatalf("Store.Get calls: got %d, want 2", storeFake.getCalls)
	}
	wantRevisions := []testutil.DesiredUpdateCall{
		{
			ID:               first.ID,
			Desired:          domain.DesiredTerminated,
			ExpectedRevision: 1,
		},
		{
			ID:               first.ID,
			Desired:          domain.DesiredTerminated,
			ExpectedRevision: 2,
		},
	}
	if !reflect.DeepEqual(storeFake.updateCalls, wantRevisions) {
		t.Fatalf("UpdateDesired calls: %#v", storeFake.updateCalls)
	}
	if calls := waker.WakeCalls(); !reflect.DeepEqual(calls, []string{first.ID}) {
		t.Fatalf("Wake calls after retry: %v", calls)
	}
}

// TestSandboxServiceDeleteStopsAfterSecondConflict 验证持续竞争不会无界重试。
func TestSandboxServiceDeleteStopsAfterSecondConflict(t *testing.T) {
	first := domain.Sandbox{
		ID:            "sandbox-1",
		DesiredState:  domain.DesiredRunning,
		ObservedState: domain.StateRunning,
		Revision:      1,
	}
	latest := first
	latest.Revision = 2
	storeFake := &deleteRetryStore{
		FakeStore:  testutil.NewFakeStore(),
		snapshots:  []domain.Sandbox{first, latest},
		updateErrs: []error{domain.ErrConflict, domain.ErrConflict},
	}
	_, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Delete(
		context.Background(),
		DeleteSandbox{SandboxID: first.ID},
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete second conflict: got %v, want ErrConflict", err)
	}
	if !reflect.DeepEqual(got, domain.Sandbox{}) {
		t.Fatalf("conflict returned partial sandbox: %#v", got)
	}
	if storeFake.getCalls != 2 || len(storeFake.updateCalls) != 2 {
		t.Fatalf(
			"unbounded or missing retry: get=%d update=%d",
			storeFake.getCalls,
			len(storeFake.updateCalls),
		)
	}
	if len(waker.WakeCalls()) != 0 {
		t.Fatalf("failed CAS called Wake: %v", waker.WakeCalls())
	}
}
