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

// TestManagerShutdownWithNoExecutionsAndRepeat 验证空 manager 和重复 shutdown 都能幂等完成并保持准入关闭。
func TestManagerShutdownWithNoExecutionsAndRepeat(t *testing.T) {
	manager, err := NewManager(2)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	for index := 0; index < 2; index++ {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown %d: %v", index, err)
		}
	}
	if _, err := manager.CreateExecution(); !errors.Is(err, ErrRunnerShuttingDown) {
		t.Fatalf("create after shutdown: %v", err)
	}
}

// TestManagerShutdownCancelsExecutionsConcurrently 验证 shutdown 并发发出 runner_shutdown，而不是串行累加清理耗时。
func TestManagerShutdownCancelsExecutionsConcurrently(t *testing.T) {
	const count = 4
	var sequence atomic.Int64
	manager, err := newManager(count, creatorFunc(func() (*Execution, error) {
		return newPendingExecution(ExecutionID("exec_shutdown_"+strconv.FormatInt(sequence.Add(1), 10)), time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	for index := 0; index < count; index++ {
		execution, createErr := manager.CreateExecution()
		if createErr != nil {
			t.Fatalf("create %d: %v", index, createErr)
		}
		if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
		id := execution.Descriptor().ID
		if err := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
			entered <- struct{}{}
			<-release
			if reason != TerminationRunnerShutdown {
				return errors.New("unexpected shutdown reason")
			}
			if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
				return err
			}
			return manager.Complete(id)
		}); err != nil {
			t.Fatalf("set handler %d: %v", index, err)
		}
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	for index := 0; index < count; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatalf("handler %d did not start concurrently", index)
		}
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if manager.activeCount() != 0 {
		t.Fatalf("active after shutdown: %d", manager.activeCount())
	}
}

// TestManagerShutdownClosesAdmissionBeforeSnapshot 验证与 CreateExecution 竞争时，新执行要么被纳入快照，要么被拒绝。
func TestManagerShutdownClosesAdmissionBeforeSnapshot(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		id := ExecutionID("exec_shutdown_race_" + strconv.Itoa(iteration))
		manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
			return newPendingExecution(id, time.Now()), nil
		}))
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		start := make(chan struct{})
		created := make(chan *Execution, 1)
		createErr := make(chan error, 1)
		shutdownErr := make(chan error, 1)
		go func() {
			<-start
			execution, err := manager.CreateExecution()
			created <- execution
			createErr <- err
		}()
		go func() {
			<-start
			shutdownErr <- manager.Shutdown(context.Background())
		}()
		close(start)
		execution := <-created
		err = <-createErr
		if err == nil {
			if bindErr := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
				if transitionErr := execution.Transition(ExecutionCancelled, reason, nil); transitionErr != nil {
					return transitionErr
				}
				return manager.Complete(id)
			}); bindErr != nil {
				t.Fatalf("bind iteration %d: %v", iteration, bindErr)
			}
		} else if !errors.Is(err, ErrRunnerShuttingDown) {
			t.Fatalf("create iteration %d: %v", iteration, err)
		}
		if err := <-shutdownErr; err != nil {
			t.Fatalf("shutdown iteration %d: %v", iteration, err)
		}
		if manager.activeCount() != 0 {
			t.Fatalf("leaked active iteration %d: %d", iteration, manager.activeCount())
		}
	}
}

// TestManagerShutdownUsesTotalTimeout 验证所有清理共享一个总 deadline，超时后后台清理仍可完成。
func TestManagerShutdownUsesTotalTimeout(t *testing.T) {
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_slow_shutdown", time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	execution, _ := manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	release := make(chan struct{})
	finished := make(chan struct{})
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		<-release
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		err := manager.Complete(execution.Descriptor().ID)
		close(finished)
		return err
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown timeout: %v", err)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cleanup stopped after caller timeout")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeat shutdown after cleanup: %v", err)
	}
}

// TestManagerConcurrentRepeatedShutdown 验证多个 shutdown 调用共享幂等取消并全部收敛。
func TestManagerConcurrentRepeatedShutdown(t *testing.T) {
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_repeat_shutdown", time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	execution, _ := manager.CreateExecution()
	var calls atomic.Int64
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		calls.Add(1)
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		return manager.Complete(execution.Descriptor().ID)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	const callers = 16
	var wait sync.WaitGroup
	errs := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- manager.Shutdown(context.Background())
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
}
