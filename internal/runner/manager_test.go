package runner

import (
	"bytes"
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
