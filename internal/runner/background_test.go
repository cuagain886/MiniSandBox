package runner

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBackgroundCoordinatorIgnoresRequestCancellation 验证创建请求立即断开后，后台 execution 仍走到自身 terminal。
func TestBackgroundCoordinatorIgnoresRequestCancellation(t *testing.T) {
	manager, execution, handlerCalls := backgroundTestExecution(t, "exec_background_detached")
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	requestContext, disconnect := context.WithCancel(context.Background())
	disconnect()
	if !errors.Is(requestContext.Err(), context.Canceled) {
		t.Fatal("request context did not cancel")
	}
	coordinator, err := StartBackgroundCoordinator(serverContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	exitCode := 0
	if err := execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if err := manager.Complete(execution.Descriptor().ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionExited || handlerCalls.Load() != 0 {
		t.Fatalf("background result: descriptor=%+v cancel_calls=%d", got, handlerCalls.Load())
	}
}

// TestBackgroundCoordinatorAllowsExplicitCancel 验证脱离请求不影响显式按 ID 取消。
func TestBackgroundCoordinatorAllowsExplicitCancel(t *testing.T) {
	manager, execution, handlerCalls := backgroundTestExecution(t, "exec_background_cancel")
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	coordinator, err := StartBackgroundCoordinator(serverContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	if err := manager.Cancel(context.Background(), execution.Descriptor().ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationExplicitCancel || handlerCalls.Load() != 1 {
		t.Fatalf("cancel result: descriptor=%+v calls=%d", got, handlerCalls.Load())
	}
}

// TestBackgroundCoordinatorCancelsOnServerShutdown 验证 runner lifetime 结束仍会取消后台 execution。
func TestBackgroundCoordinatorCancelsOnServerShutdown(t *testing.T) {
	manager, execution, handlerCalls := backgroundTestExecution(t, "exec_background_shutdown")
	serverContext, stopServer := context.WithCancel(context.Background())
	coordinator, err := StartBackgroundCoordinator(serverContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	stopServer()
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationRunnerShutdown || handlerCalls.Load() != 1 {
		t.Fatalf("shutdown result: descriptor=%+v calls=%d", got, handlerCalls.Load())
	}
}

// TestBackgroundCoordinatorCreationFailureStartsNothing 验证未知 execution 或未绑定 handler 时不会启动泄漏的监听 goroutine。
func TestBackgroundCoordinatorCreationFailureStartsNothing(t *testing.T) {
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_unready_background", time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	if _, err := StartBackgroundCoordinator(serverContext, manager, "exec_unknown"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("unknown execution: %v", err)
	}
	execution, _ := manager.CreateExecution()
	if _, err := StartBackgroundCoordinator(serverContext, manager, execution.Descriptor().ID); err == nil {
		t.Fatal("coordinator accepted execution without cancellation handler")
	}
}

// TestBackgroundCoordinatorTerminalShutdownRace 验证自然退出与 server shutdown 竞态仍由 arbiter 选择唯一终态。
func TestBackgroundCoordinatorTerminalShutdownRace(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		execution := newPendingExecution(ExecutionID("exec_background_race_"+strconv.Itoa(iteration)), time.Now())
		manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		_, _ = manager.CreateExecution()
		_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
		store := newStartedEventStore(t, 1024)
		finalized := make(chan struct{})
		var finalize sync.Once
		arbiter, err := NewTerminalArbiter(execution, store, finalized)
		if err != nil {
			t.Fatalf("new arbiter: %v", err)
		}
		id := execution.Descriptor().ID
		if err := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
			_, submitErr := arbiter.Submit(context.Background(), TerminalCandidate{Reason: reason})
			finalize.Do(func() { close(finalized) })
			if submitErr != nil {
				return submitErr
			}
			if waitErr := arbiter.Wait(context.Background()); waitErr != nil {
				return waitErr
			}
			return manager.Complete(id)
		}); err != nil {
			t.Fatalf("set handler: %v", err)
		}
		serverContext, stopServer := context.WithCancel(context.Background())
		coordinator, err := StartBackgroundCoordinator(serverContext, manager, id)
		if err != nil {
			t.Fatalf("start coordinator: %v", err)
		}
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			exitCode := 0
			_, _ = arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode})
			finalize.Do(func() { close(finalized) })
			_ = arbiter.Wait(context.Background())
			_ = manager.Complete(id)
		}()
		go func() {
			defer wait.Done()
			stopServer()
		}()
		wait.Wait()
		if err := coordinator.Wait(context.Background()); err != nil {
			t.Fatalf("wait coordinator: %v", err)
		}
		events := store.Events()
		terminalCount := 0
		for _, event := range events {
			if event.Terminal() {
				terminalCount++
			}
		}
		if terminalCount != 1 {
			t.Fatalf("terminal events: %+v", events)
		}
		store.Close()
	}
}

func backgroundTestExecution(t *testing.T, id ExecutionID) (*Manager, *Execution, *atomic.Int64) {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	calls := &atomic.Int64{}
	if err := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
		calls.Add(1)
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		return manager.Complete(id)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	return manager, execution, calls
}
