package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestExecutionStatusHandlerReturnsEveryState 验证六种状态映射，终态包含唯一 terminal metadata。
func TestExecutionStatusHandlerReturnsEveryState(t *testing.T) {
	states := []ExecutionState{ExecutionPending, ExecutionRunning, ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			manager, id := statusManagerInState(t, state)
			handler, _ := NewExecutionStatusHandler(manager)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, executionStatusPathPrefix+string(id), nil))
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var status protocol.ExecutionStatus
			if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
				t.Fatalf("decode: %v", err)
			}
			want, _ := MapExecutionState(state)
			if status.ExecutionID != string(id) || status.State != want || (terminalExecutionState(state) != (status.TerminalEvent != nil)) {
				t.Fatalf("status: %+v", status)
			}
		})
	}
}

// TestExecutionStatusHandlerRejectsMethodUnknownAndInvalidPath 验证 method、未知 ID 和非单段路径。
func TestExecutionStatusHandlerRejectsMethodUnknownAndInvalidPath(t *testing.T) {
	manager, id := statusManagerInState(t, ExecutionPending)
	handler, _ := NewExecutionStatusHandler(manager)
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, executionStatusPathPrefix + string(id), http.StatusMethodNotAllowed},
		{http.MethodGet, executionStatusPathPrefix + "exec_unknown", http.StatusNotFound},
		{http.MethodGet, executionStatusPathPrefix, http.StatusNotFound},
		{http.MethodGet, executionStatusPathPrefix + "a/b", http.StatusNotFound},
		{http.MethodGet, executionStatusPathPrefix + "a%2Fb", http.StatusNotFound},
		{http.MethodGet, executionStatusPathPrefix + "a%0Ab", http.StatusNotFound},
	}
	for index, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("case %d: status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
}

// TestExecutionStatusHandlerRedactsExecutionInternals 验证状态响应不包含命令、env、PID/PGID 或内部原因。
func TestExecutionStatusHandlerRedactsExecutionInternals(t *testing.T) {
	manager, id := statusManagerInState(t, ExecutionCancelled)
	handler, _ := NewExecutionStatusHandler(manager)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, executionStatusPathPrefix+string(id), nil))
	body := response.Body.String()
	for _, forbidden := range []string{"argv", "env", "pid", "pgid", string(TerminationRunnerShutdown), "secret"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
}

// TestExecutionStatusSnapshotStaysConsistentDuringTerminalPublish 验证状态先变、terminal 后发布的窗口只返回旧非终态或完整终态。
func TestExecutionStatusSnapshotStaysConsistentDuringTerminalPublish(t *testing.T) {
	execution := newPendingExecution("exec_status_race", time.Now())
	manager, _ := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	_, _ = manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	store, _ := NewEventStore(execution.Descriptor().ID, systemClock{}, 1024)
	defer store.Close()
	_ = manager.SetEventStore(execution.Descriptor().ID, store)
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	if snapshot, err := manager.StatusSnapshot(execution.Descriptor().ID); err != nil || snapshot.Descriptor.State != ExecutionRunning {
		t.Fatalf("prime visible state: snapshot=%+v err=%v", snapshot, err)
	}
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		<-start
		exitCode := 0
		_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
		time.Sleep(time.Millisecond)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
		close(done)
	}()
	close(start)
	for {
		snapshot, err := manager.StatusSnapshot(execution.Descriptor().ID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if snapshot.Descriptor.State == ExecutionExited && snapshot.TerminalEvent == nil {
			t.Fatalf("inconsistent terminal snapshot: %+v", snapshot)
		}
		select {
		case <-done:
			final, _ := manager.StatusSnapshot(execution.Descriptor().ID)
			if final.Descriptor.State != ExecutionExited || final.TerminalEvent == nil {
				t.Fatalf("final snapshot: %+v", final)
			}
			return
		default:
		}
	}
}

func statusManagerInState(t *testing.T, state ExecutionState) (*Manager, ExecutionID) {
	t.Helper()
	id := ExecutionID("exec_status_" + state)
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	store, err := NewEventStore(id, systemClock{}, 1024)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	_ = manager.SetEventStore(id, store)
	if state == ExecutionPending {
		return manager, id
	}
	if state != ExecutionFailed {
		_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	}
	duration := durationPointer(1)
	switch state {
	case ExecutionRunning:
		return manager, id
	case ExecutionExited:
		exitCode := 7
		_ = execution.Transition(state, TerminationProcessExited, &exitCode)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: duration})
	case ExecutionFailed:
		_ = execution.Transition(state, TerminationStartFailed, nil)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventFailed, ErrorCode: "START_FAILED", Message: "execution could not start", DurationMS: duration})
	case ExecutionCancelled:
		_ = execution.Transition(state, TerminationRunnerShutdown, nil)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventCancelled, DurationMS: duration})
	case ExecutionTimedOut:
		_ = execution.Transition(state, TerminationDeadlineExceeded, nil)
		_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventTimedOut, DurationMS: duration})
	}
	_ = manager.Complete(id)
	return manager, id
}
