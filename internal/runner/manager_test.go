package runner

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type creatorFunc func() (*Execution, error)

func (f creatorFunc) New() (*Execution, error) { return f() }

// TestManagerCreatesAndQueriesSnapshot 验证注册 Pending execution 后只能通过描述符快照查询。
func TestManagerCreatesAndQueriesSnapshot(t *testing.T) {
	factory := newExecutionFactory(bytes.NewReader(make([]byte, 32)), fixedClock{value: time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)})
	manager, err := newManager(2, factory)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	execution, err := manager.CreateExecution()
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	descriptor, err := manager.Descriptor(execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("query descriptor: %v", err)
	}
	if descriptor.State != ExecutionPending || descriptor.ID == "" {
		t.Fatalf("descriptor: %+v", descriptor)
	}
	descriptor.State = ExecutionFailed
	again, err := manager.Descriptor(descriptor.ID)
	if err != nil || again.State != ExecutionPending {
		t.Fatalf("descriptor aliases manager state: descriptor=%+v err=%v", again, err)
	}
	if _, err := manager.Descriptor("exec_unknown"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("unknown query: %v", err)
	}
}

// TestManagerRejectsDuplicateID 验证重复 ID 不会覆盖已有记录且会释放预占槽位。
func TestManagerRejectsDuplicateID(t *testing.T) {
	createdAt := time.Now().UTC()
	manager, err := newManager(2, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_duplicate", createdAt), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.CreateExecution(); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := manager.CreateExecution(); !errors.Is(err, ErrExecutionAlreadyRegistered) {
		t.Fatalf("duplicate create: %v", err)
	}
	if manager.activeCount() != 1 {
		t.Fatalf("active count after duplicate: %d", manager.activeCount())
	}
}

// TestManagerReleasesStartupFailures 验证 factory 失败和已注册 execution 启动失败均释放并发槽位。
func TestManagerReleasesStartupFailures(t *testing.T) {
	var calls int
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("random unavailable")
		}
		return newPendingExecution(ExecutionID("exec_"+string(rune('0'+calls))), time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.CreateExecution(); err == nil {
		t.Fatal("factory failure was accepted")
	}
	execution, err := manager.CreateExecution()
	if err != nil {
		t.Fatalf("create after factory failure: %v", err)
	}
	if err := execution.Transition(ExecutionFailed, TerminationStartFailed, nil); err != nil {
		t.Fatalf("transition start failure: %v", err)
	}
	if err := manager.Complete(execution.Descriptor().ID); err != nil {
		t.Fatalf("complete start failure: %v", err)
	}
	if err := manager.Complete(execution.Descriptor().ID); err != nil {
		t.Fatalf("repeat complete: %v", err)
	}
	if _, err := manager.CreateExecution(); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	failed, err := manager.Descriptor(execution.Descriptor().ID)
	if err != nil || failed.State != ExecutionFailed {
		t.Fatalf("terminal record not retained: descriptor=%+v err=%v", failed, err)
	}
}

// TestManagerEnforcesConcurrentLimit 验证上限边界返回稳定 EXECUTION_LIMIT_REACHED。
func TestManagerEnforcesConcurrentLimit(t *testing.T) {
	var sequence atomic.Int64
	manager, err := newManager(2, creatorFunc(func() (*Execution, error) {
		return newPendingExecution(ExecutionID("exec_limit_"+time.Now().Add(time.Duration(sequence.Add(1))).Format("150405.000000000")), time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	for index := 0; index < 2; index++ {
		if _, err := manager.CreateExecution(); err != nil {
			t.Fatalf("create %d: %v", index, err)
		}
	}
	if _, err := manager.CreateExecution(); !errors.Is(err, ErrExecutionLimitReached) || err.Error() != "EXECUTION_LIMIT_REACHED" {
		t.Fatalf("limit error: %v", err)
	}
}

// TestManagerConcurrentCreateNeverExceedsLimit 验证并发注册先占槽，任何时刻都不会越过上限。
func TestManagerConcurrentCreateNeverExceedsLimit(t *testing.T) {
	const limit = 4
	var sequence atomic.Int64
	manager, err := newManager(limit, creatorFunc(func() (*Execution, error) {
		id := sequence.Add(1)
		return newPendingExecution(ExecutionID("exec_race_"+strconv.FormatInt(id, 10)), time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	var wait sync.WaitGroup
	var successes atomic.Int64
	var wrongErrors atomic.Int64
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, createErr := manager.CreateExecution()
			if createErr == nil {
				successes.Add(1)
			} else if !errors.Is(createErr, ErrExecutionLimitReached) {
				wrongErrors.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != limit || wrongErrors.Load() != 0 || manager.activeCount() != limit {
		t.Fatalf("concurrent result: successes=%d wrong=%d active=%d", successes.Load(), wrongErrors.Load(), manager.activeCount())
	}
}

func (m *Manager) activeCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// TestManagerCancelsPendingAndRunningOnce 验证 Pending/Running 首次取消均被接受，且重复请求不重复执行 handler。
func TestManagerCancelsPendingAndRunningOnce(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "running"}[running], func(t *testing.T) {
			var sequence atomic.Int64
			manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
				return newPendingExecution(ExecutionID("exec_cancel_"+strconv.FormatInt(sequence.Add(1), 10)), time.Now()), nil
			}))
			if err != nil {
				t.Fatalf("new manager: %v", err)
			}
			execution, err := manager.CreateExecution()
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if running {
				if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
					t.Fatalf("start: %v", err)
				}
			}
			var calls atomic.Int64
			if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
				calls.Add(1)
				if reason != TerminationExplicitCancel {
					t.Fatalf("reason: %q", reason)
				}
				if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
					return err
				}
				return manager.Complete(execution.Descriptor().ID)
			}); err != nil {
				t.Fatalf("set handler: %v", err)
			}
			for index := 0; index < 2; index++ {
				if err := manager.Cancel(context.Background(), execution.Descriptor().ID); err != nil {
					t.Fatalf("cancel %d: %v", index, err)
				}
			}
			if calls.Load() != 1 || execution.Descriptor().State != ExecutionCancelled || manager.activeCount() != 0 {
				t.Fatalf("cancel result: calls=%d descriptor=%+v active=%d", calls.Load(), execution.Descriptor(), manager.activeCount())
			}
		})
	}
}

// TestManagerQueuesCancelUntilHandlerIsReady 验证启动窗口内的 Pending cancel 不会因 handler 尚未绑定而丢失。
func TestManagerQueuesCancelUntilHandlerIsReady(t *testing.T) {
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_pending_cancel", time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	execution, err := manager.CreateExecution()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := manager.Cancel(context.Background(), execution.Descriptor().ID); err != nil {
		t.Fatalf("queue cancel: %v", err)
	}
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
	if calls.Load() != 1 || execution.Descriptor().State != ExecutionCancelled {
		t.Fatalf("queued cancel result: calls=%d descriptor=%+v", calls.Load(), execution.Descriptor())
	}
}

// TestManagerCancelTerminalAndUnknown 验证所有终态取消均为 no-op，未知 ID 明确报错。
func TestManagerCancelTerminalAndUnknown(t *testing.T) {
	states := []ExecutionState{ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			// Manager 只允许注册 Pending，先注册后再复现目标终态。
			execution := newPendingExecution(ExecutionID("exec_terminal_"+state), time.Now())
			manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
			if err != nil {
				t.Fatalf("new manager: %v", err)
			}
			if _, err := manager.CreateExecution(); err != nil {
				t.Fatalf("create: %v", err)
			}
			if state != ExecutionFailed {
				if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
					t.Fatalf("start: %v", err)
				}
			}
			switch state {
			case ExecutionExited:
				exitCode := 0
				_ = execution.Transition(state, TerminationProcessExited, &exitCode)
			case ExecutionFailed:
				_ = execution.Transition(state, TerminationStartFailed, nil)
			case ExecutionCancelled:
				_ = execution.Transition(state, TerminationExplicitCancel, nil)
			case ExecutionTimedOut:
				_ = execution.Transition(state, TerminationDeadlineExceeded, nil)
			}
			var calls atomic.Int64
			if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(TerminationReason) error { calls.Add(1); return nil }); err != nil {
				t.Fatalf("set handler: %v", err)
			}
			if err := manager.Cancel(context.Background(), execution.Descriptor().ID); err != nil || calls.Load() != 0 {
				t.Fatalf("terminal cancel: err=%v calls=%d", err, calls.Load())
			}
		})
	}
	manager, _ := newManager(1, creatorFunc(func() (*Execution, error) { return nil, errors.New("unused") }))
	if err := manager.Cancel(context.Background(), "exec_unknown"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("unknown cancel: %v", err)
	}
}

// TestManagerConcurrentRepeatedCancel 验证并发重复取消共享同一次清理结果，不暴露或复制内部 goroutine。
func TestManagerConcurrentRepeatedCancel(t *testing.T) {
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) {
		return newPendingExecution("exec_concurrent_cancel", time.Now()), nil
	}))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	execution, _ := manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	release := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int64
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		calls.Add(1)
		close(entered)
		<-release
		return execution.Transition(ExecutionCancelled, reason, nil)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	const callers = 32
	errorsSeen := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() { errorsSeen <- manager.Cancel(context.Background(), execution.Descriptor().ID) }()
	}
	<-entered
	close(release)
	for index := 0; index < callers; index++ {
		if err := <-errorsSeen; err != nil {
			t.Fatalf("cancel %d: %v", index, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
}
