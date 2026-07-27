package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"minisandbox/internal/domain"
	"minisandbox/internal/testutil"
)

// TestSandboxServiceGet 验证普通查询只读取一次 Store 并返回完整领域对象。
func TestSandboxServiceGet(t *testing.T) {
	storeFake, idGenerator, clock, builder, waker :=
		newSandboxServiceTestDependencies()
	want := domain.Sandbox{
		ID:            "sandbox-1",
		DesiredState:  domain.DesiredRunning,
		ObservedState: domain.StateRunning,
		Revision:      4,
	}
	storeFake.SetGetResult(want, nil)
	service := NewSandboxService(storeFake, idGenerator, clock, builder, waker)

	got, err := service.Get(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox mismatch: got %#v, want %#v", got, want)
	}
	if calls := storeFake.GetCalls(); !reflect.DeepEqual(calls, []string{want.ID}) {
		t.Fatalf("Store.Get calls: got %v, want [%s]", calls, want.ID)
	}
}

// TestSandboxServiceGetErrors 验证不存在和 Store 不可用分类保持且不返回部分对象。
func TestSandboxServiceGetErrors(t *testing.T) {
	unavailable := errors.New("store unavailable")
	tests := []struct {
		name string
		err  error
	}{
		{name: "not found", err: domain.ErrNotFound},
		{name: "unavailable", err: unavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeFake := testutil.NewFakeStore()
			storeFake.SetGetResult(
				domain.Sandbox{ID: "must-not-return"},
				tt.err,
			)
			_, idGenerator, clock, builder, waker :=
				newSandboxServiceTestDependencies()
			service := NewSandboxService(
				storeFake,
				idGenerator,
				clock,
				builder,
				waker,
			)

			got, err := service.Get(context.Background(), "sandbox-1")
			if !errors.Is(err, tt.err) {
				t.Fatalf("get error: got %v, want %v", err, tt.err)
			}
			if !reflect.DeepEqual(got, domain.Sandbox{}) {
				t.Fatalf("error returned partial sandbox: %#v", got)
			}
			if calls := storeFake.GetCalls(); !reflect.DeepEqual(
				calls,
				[]string{"sandbox-1"},
			) {
				t.Fatalf("Store.Get calls: %v", calls)
			}
		})
	}
}
