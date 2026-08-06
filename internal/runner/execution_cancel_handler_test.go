package runner

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestExecutionCancelHandlerReturnsBeforeTerminationCompletes 验证首次和重复 DELETE 返回 202，且不等待 grace/KILL。
func TestExecutionCancelHandlerReturnsBeforeTerminationCompletes(t *testing.T) {
	manager, execution := cancelHandlerExecution(t, "exec_cancel_async")
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		calls.Add(1)
		close(entered)
		<-release
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		return manager.Complete(execution.Descriptor().ID)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	handler, _ := NewExecutionCancelHandler(manager)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodDelete, executionStatusPathPrefix+string(execution.Descriptor().ID), nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status: %d body=%s", first.Code, first.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not start")
	}
	repeat := httptest.NewRecorder()
	handler.ServeHTTP(repeat, httptest.NewRequest(http.MethodDelete, executionStatusPathPrefix+string(execution.Descriptor().ID), nil))
	if repeat.Code != http.StatusAccepted || execution.Descriptor().State != ExecutionRunning {
		t.Fatalf("repeat: status=%d state=%s", repeat.Code, execution.Descriptor().State)
	}
	close(release)
	eventually(t, time.Second, func() bool { return execution.Descriptor().State == ExecutionCancelled })
	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
}

// TestExecutionCancelHandlerReturns204ForEveryTerminal 验证所有终态 DELETE 都是无 body 的 204 no-op。
func TestExecutionCancelHandlerReturns204ForEveryTerminal(t *testing.T) {
	for _, state := range []ExecutionState{ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut} {
		t.Run(string(state), func(t *testing.T) {
			manager, id := statusManagerInState(t, state)
			handler, _ := NewExecutionCancelHandler(manager)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, executionStatusPathPrefix+string(id), nil))
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("response: status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

// TestExecutionCancelHandlerRejectsUnknownInvalidAndMethod 验证未知/非法 ID 为 404，错误 method 为 405。
func TestExecutionCancelHandlerRejectsUnknownInvalidAndMethod(t *testing.T) {
	manager, execution := cancelHandlerExecution(t, "exec_cancel_routes")
	_ = manager.SetCancellationHandler(execution.Descriptor().ID, func(TerminationReason) error { return nil })
	handler, _ := NewExecutionCancelHandler(manager)
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, executionStatusPathPrefix + string(execution.Descriptor().ID), http.StatusMethodNotAllowed},
		{http.MethodDelete, executionStatusPathPrefix + "exec_unknown", http.StatusNotFound},
		{http.MethodDelete, executionStatusPathPrefix + "a/b", http.StatusNotFound},
	}
	for index, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("case %d: status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
}

// TestExecutionCancelHandlerConcurrentDeleteStartsOneCancellation 验证并发 DELETE 全部重试安全且只选择一次取消路径。
func TestExecutionCancelHandlerConcurrentDeleteStartsOneCancellation(t *testing.T) {
	manager, execution := cancelHandlerExecution(t, "exec_cancel_concurrent")
	release := make(chan struct{})
	var calls atomic.Int64
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		calls.Add(1)
		<-release
		return execution.Transition(ExecutionCancelled, reason, nil)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	handler, _ := NewExecutionCancelHandler(manager)
	const count = 32
	statuses := make(chan int, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, executionStatusPathPrefix+string(execution.Descriptor().ID), nil))
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusAccepted {
			t.Fatalf("status: %d", status)
		}
	}
	close(release)
	eventually(t, time.Second, func() bool { return execution.Descriptor().State == ExecutionCancelled })
	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
}

func cancelHandlerExecution(t *testing.T, id ExecutionID) (*Manager, *Execution) {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	return manager, execution
}
