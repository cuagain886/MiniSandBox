package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// fakeExecutionServer 驱动后台 execution 的状态与日志脚本，用于验收高层
// 易用接口，不依赖真实 runner。
type fakeExecutionServer struct {
	mu          sync.Mutex
	statuses    []protocol.ExecutionStatus
	statusIndex int
	logPages    map[uint64]protocol.ExecutionLogPage
	cancels     int
}

func (f *fakeExecutionServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes/sbx-run/executions":
			var request protocol.ExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode execute request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if !request.Background {
				t.Errorf("high-level execution must run in background mode")
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(protocol.ExecutionDescriptor{
				ExecutionID: "exec-run",
				State:       protocol.ExecutionStatePending,
			})
		case r.Method == http.MethodDelete:
			f.cancels++
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Query().Has("cursor"):
			cursor, err := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
			if err != nil {
				http.Error(w, "bad cursor", http.StatusBadRequest)
				return
			}
			page, ok := f.logPages[cursor]
			if !ok {
				page = protocol.ExecutionLogPage{NextCursor: cursor}
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodGet:
			// 每次查询按脚本前进一步，耗尽后重复最后一项（终态）。
			index := f.statusIndex
			if index >= len(f.statuses) {
				index = len(f.statuses) - 1
			}
			f.statusIndex++
			_ = json.NewEncoder(w).Encode(f.statuses[index])
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

func exitedStatus(state protocol.ExecutionState, eventType protocol.EventType, exitCode *int) protocol.ExecutionStatus {
	terminal := &protocol.ExecutionEvent{
		ExecutionID:     "exec-run",
		Sequence:        4,
		Timestamp:       time.Unix(1003, 0).UTC(),
		Type:            eventType,
		DurationMS:      &[]int64{1200}[0],
		OutputTruncated: &[]bool{false}[0],
		ExitCode:        exitCode,
	}
	if eventType == protocol.EventFailed {
		terminal.ExitCode = nil
		terminal.ErrorCode = "SPAWN_FAILED"
		terminal.Message = "spawn failed"
	}
	return protocol.ExecutionStatus{
		ExecutionID:   "exec-run",
		State:         state,
		TerminalEvent: terminal,
	}
}

func completedLogPages() map[uint64]protocol.ExecutionLogPage {
	return map[uint64]protocol.ExecutionLogPage{
		0: {
			Events: []protocol.ExecutionEvent{
				{
					ExecutionID: "exec-run",
					Sequence:    1,
					Timestamp:   time.Unix(1000, 0).UTC(),
					Type:        protocol.EventStarted,
				},
				{
					ExecutionID: "exec-run",
					Sequence:    2,
					Timestamp:   time.Unix(1001, 0).UTC(),
					Type:        protocol.EventStdout,
					DataBase64:  base64.StdEncoding.EncodeToString([]byte("out-1;")),
				},
			},
			NextCursor: 2,
			Complete:   false,
		},
		2: {
			Events: []protocol.ExecutionEvent{
				{
					ExecutionID: "exec-run",
					Sequence:    3,
					Timestamp:   time.Unix(1002, 0).UTC(),
					Type:        protocol.EventStderr,
					DataBase64:  base64.StdEncoding.EncodeToString([]byte("err-1")),
				},
			},
			NextCursor: 3,
			Complete:   false,
		},
		3: {
			Events: []protocol.ExecutionEvent{
				{
					ExecutionID:     "exec-run",
					Sequence:        4,
					Timestamp:       time.Unix(1003, 0).UTC(),
					Type:            protocol.EventExited,
					ExitCode:        &[]int{0}[0],
					DurationMS:      &[]int64{1200}[0],
					OutputTruncated: &[]bool{false}[0],
				},
			},
			NextCursor: 4,
			Complete:   true,
		},
	}
}

// TestExecutionFacadeCollectsDecodedLogs 验收 StartExecution → Wait → Logs：
// 状态等待收敛、日志自动翻页并解码、不再接触 cursor 与 Base64。
func TestExecutionFacadeCollectsDecodedLogs(t *testing.T) {
	fake := &fakeExecutionServer{
		statuses: []protocol.ExecutionStatus{
			{ExecutionID: "exec-run", State: protocol.ExecutionStatePending},
			{ExecutionID: "exec-run", State: protocol.ExecutionStateRunning},
			exitedStatus(protocol.ExecutionStateExited, protocol.EventExited, &[]int{0}[0]),
		},
		logPages: completedLogPages(),
	}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox := client.Sandbox("sbx-run")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	execution, err := sandbox.StartExecution(ctx, ExecuteRequest{
		Argv: []string{"/bin/true"}, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if execution.ID() != "exec-run" {
		t.Fatalf("unexpected execution ID: %s", execution.ID())
	}

	info, err := execution.Wait(ctx)
	if err != nil {
		t.Fatalf("wait execution: %v", err)
	}
	if info.State != ExecutionStateExited || info.TerminalEvent == nil {
		t.Fatalf("unexpected terminal info: %#v", info)
	}

	var types []EventType
	var stdout, stderr string
	logs := execution.Logs(ctx, 0)
	for logs.Next() {
		event := logs.Event()
		types = append(types, event.Type)
		switch event.Type {
		case EventStdout:
			stdout += string(event.Data)
		case EventStderr:
			stderr += string(event.Data)
		}
	}
	if err := logs.Err(); err != nil {
		t.Fatalf("iterate logs: %v", err)
	}
	if stdout != "out-1;" || stderr != "err-1" {
		t.Fatalf("decoded output mismatch: stdout=%q stderr=%q", stdout, stderr)
	}
	if len(types) != 4 || types[0] != EventStarted || types[3] != EventExited {
		t.Fatalf("unexpected event order: %v", types)
	}
}

// runScenario 封装一次 Run 验收：给定终态脚本，校验 RunResult 与返回错误。
func runScenario(
	t *testing.T,
	name string,
	statuses []protocol.ExecutionStatus,
	verify func(*testing.T, RunResult, error),
) {
	t.Run(name, func(t *testing.T) {
		fake := &fakeExecutionServer{statuses: statuses, logPages: completedLogPages()}
		server := httptest.NewServer(fake.handler(t))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := client.Sandbox("sbx-run").Run(ctx, ExecuteRequest{
			Argv: []string{"/bin/true"},
		})
		verify(t, result, err)
	})
}

// TestRunScenarios 验收 Run 的正常退出、非零退出、取消、超时与失败语义。
func TestRunScenarios(t *testing.T) {
	runScenario(t, "zero-exit",
		[]protocol.ExecutionStatus{
			exitedStatus(protocol.ExecutionStateExited, protocol.EventExited, &[]int{0}[0]),
		},
		func(t *testing.T, result RunResult, err error) {
			if err != nil {
				t.Fatalf("run should succeed: %v", err)
			}
			if result.ExitCode != 0 || string(result.Stdout) != "out-1;" ||
				string(result.Stderr) != "err-1" || result.Duration != 1200*time.Millisecond {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	runScenario(t, "nonzero-exit",
		[]protocol.ExecutionStatus{
			exitedStatus(protocol.ExecutionStateExited, protocol.EventExited, &[]int{7}[0]),
		},
		func(t *testing.T, result RunResult, err error) {
			var exitErr *ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode != 7 {
				t.Fatalf("expected ExitError with code 7, got %v", err)
			}
			if result.ExitCode != 7 || string(result.Stdout) != "out-1;" {
				t.Fatalf("result should still carry output: %#v", result)
			}
		})
	runScenario(t, "cancelled",
		[]protocol.ExecutionStatus{
			exitedStatus(protocol.ExecutionStateCancelled, protocol.EventCancelled, nil),
		},
		func(t *testing.T, result RunResult, err error) {
			var cancelErr *ExecutionCancelledError
			if !errors.As(err, &cancelErr) {
				t.Fatalf("expected ExecutionCancelledError, got %v", err)
			}
			if result.ExitCode != -1 || result.State != ExecutionStateCancelled {
				t.Fatalf("unexpected cancelled result: %#v", result)
			}
		})
	runScenario(t, "timed-out",
		[]protocol.ExecutionStatus{
			exitedStatus(protocol.ExecutionStateTimedOut, protocol.EventTimedOut, nil),
		},
		func(t *testing.T, result RunResult, err error) {
			var timeoutErr *ExecutionTimedOutError
			if !errors.As(err, &timeoutErr) {
				t.Fatalf("expected ExecutionTimedOutError, got %v", err)
			}
			if result.State != ExecutionStateTimedOut {
				t.Fatalf("unexpected timed-out result: %#v", result)
			}
		})
	runScenario(t, "failed",
		[]protocol.ExecutionStatus{
			exitedStatus(protocol.ExecutionStateFailed, protocol.EventFailed, nil),
		},
		func(t *testing.T, result RunResult, err error) {
			var failedErr *ExecutionFailedError
			if !errors.As(err, &failedErr) || failedErr.ErrorCode != "SPAWN_FAILED" {
				t.Fatalf("expected ExecutionFailedError, got %v", err)
			}
		})
}

// TestCancelAndWaitConverges 验收取消后等待：DELETE 提交一次，终态收敛。
func TestCancelAndWaitConverges(t *testing.T) {
	fake := &fakeExecutionServer{
		statuses: []protocol.ExecutionStatus{
			{ExecutionID: "exec-run", State: protocol.ExecutionStateRunning},
			exitedStatus(protocol.ExecutionStateCancelled, protocol.EventCancelled, nil),
		},
		logPages: completedLogPages(),
	}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	execution, err := client.Sandbox("sbx-run").StartExecution(ctx, ExecuteRequest{
		Shell: "sleep 30",
	})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	info, err := execution.CancelAndWait(ctx)
	if err != nil {
		t.Fatalf("cancel and wait: %v", err)
	}
	if info.State != ExecutionStateCancelled || fake.cancels != 1 {
		t.Fatalf("unexpected cancel convergence: state=%s cancels=%d", info.State, fake.cancels)
	}
}
