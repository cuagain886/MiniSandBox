package runner

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestForegroundCoordinatorCancelsOnRequestDisconnect 验证请求 context 消失映射为安全的 foreground_disconnect。
func TestForegroundCoordinatorCancelsOnRequestDisconnect(t *testing.T) {
	manager, execution := foregroundTestExecution(t, "exec_request_disconnect")
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	requestContext, disconnect := context.WithCancel(context.Background())
	coordinator, err := StartForegroundCoordinator(serverContext, requestContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	disconnect()
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationForegroundDisconnect {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestForegroundCoordinatorCancelsOnServerShutdown 验证 runner server context 消失映射为 runner_shutdown。
func TestForegroundCoordinatorCancelsOnServerShutdown(t *testing.T) {
	manager, execution := foregroundTestExecution(t, "exec_server_shutdown")
	serverContext, stopServer := context.WithCancel(context.Background())
	requestContext, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	coordinator, err := StartForegroundCoordinator(serverContext, requestContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	stopServer()
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationRunnerShutdown {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestForegroundCoordinatorIgnoresDisconnectAfterTerminal 验证正常 terminal 已完成后再断开请求不会改写终态。
func TestForegroundCoordinatorIgnoresDisconnectAfterTerminal(t *testing.T) {
	manager, execution := foregroundTestExecution(t, "exec_normal_then_disconnect")
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	requestContext, disconnect := context.WithCancel(context.Background())
	coordinator, err := StartForegroundCoordinator(serverContext, requestContext, manager, execution.Descriptor().ID)
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
	disconnect()
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionExited || got.TerminationReason != TerminationProcessExited {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestForegroundCoordinatorTerminalDisconnectRace 验证正常退出与断开竞态仍只发布一个 terminal。
func TestForegroundCoordinatorTerminalDisconnectRace(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		finalized := make(chan struct{})
		var finalize sync.Once
		manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
			pending := newPendingExecution(ExecutionID("exec_foreground_race_"+strconv.Itoa(iteration)), time.Now())
			return pending, nil
		}))
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		registered, err := manager.CreateExecution()
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		execution := registered
		if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		store := newStartedEventStore(t, 1024)
		arbiter, err := NewTerminalArbiter(execution, store, finalized)
		if err != nil {
			t.Fatalf("new registered arbiter: %v", err)
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
		requestContext, disconnect := context.WithCancel(context.Background())
		coordinator, err := StartForegroundCoordinator(serverContext, requestContext, manager, id)
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
			disconnect()
		}()
		wait.Wait()
		if err := coordinator.Wait(context.Background()); err != nil {
			t.Fatalf("wait coordinator: %v", err)
		}
		stopServer()
		events := store.Events()
		terminals := 0
		for _, event := range events {
			if event.Terminal() {
				terminals++
			}
		}
		if terminals != 1 || (events[len(events)-1].Type != protocol.EventExited && events[len(events)-1].Type != protocol.EventCancelled) {
			t.Fatalf("terminal race events: %+v", events)
		}
		store.Close()
	}
}

func foregroundTestExecution(t *testing.T, id ExecutionID) (*Manager, *Execution) {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.CreateExecution(); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	var calls atomic.Int64
	if err := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
		if calls.Add(1) != 1 {
			return errors.New("cancellation handler called more than once")
		}
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		return manager.Complete(id)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	return manager, execution
}
