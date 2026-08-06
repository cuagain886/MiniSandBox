package runner

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

type controlledStreamWriter struct {
	mu           sync.Mutex
	header       http.Header
	status       int
	body         bytes.Buffer
	flushes      int
	deadlines    int
	writeErr     error
	blockWrite   <-chan struct{}
	writeEntered chan<- struct{}
}

func newControlledStreamWriter() *controlledStreamWriter {
	return &controlledStreamWriter{header: make(http.Header)}
}

func (w *controlledStreamWriter) Header() http.Header { return w.header }
func (w *controlledStreamWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}
func (w *controlledStreamWriter) Write(data []byte) (int, error) {
	if w.writeEntered != nil {
		select {
		case w.writeEntered <- struct{}{}:
		default:
		}
	}
	if w.blockWrite != nil {
		<-w.blockWrite
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.body.Write(data)
}
func (w *controlledStreamWriter) FlushError() error {
	w.mu.Lock()
	w.flushes++
	w.mu.Unlock()
	return nil
}
func (w *controlledStreamWriter) SetWriteDeadline(time.Time) error {
	w.mu.Lock()
	w.deadlines++
	w.mu.Unlock()
	return nil
}
func (w *controlledStreamWriter) snapshot() (string, int, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String(), w.status, w.flushes, w.deadlines
}

// TestForegroundEventStreamSendsOrderedFramesThroughTerminal 验证 headers、顺序、逐帧 flush 和 terminal 后结束。
func TestForegroundEventStreamSendsOrderedFramesThroughTerminal(t *testing.T) {
	manager, execution, store, handle := streamTestFixture(t, "exec_stream_order")
	publishStartedOutputExit(t, execution, manager, store)
	stream, err := NewForegroundEventStream(context.Background(), manager, time.Second, time.Minute)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	writer := newControlledStreamWriter()
	request, cancel := requestWithCancel()
	defer cancel()
	stream.Serve(writer, request, handle)
	body, status, flushes, deadlines := writer.snapshot()
	if status != http.StatusOK || writer.Header().Get("Content-Type") != "text/event-stream" || writer.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("response: status=%d headers=%v", status, writer.Header())
	}
	if !strings.Contains(body, "id: 1\nevent: started") || !strings.Contains(body, "id: 2\nevent: stdout") || !strings.Contains(body, "id: 3\nevent: exited") {
		t.Fatalf("ordered body: %q", body)
	}
	if flushes != 3 || deadlines < 3 || strings.LastIndex(body, "event: exited") < strings.LastIndex(body, "event: stdout") {
		t.Fatalf("transport: flushes=%d deadlines=%d body=%q", flushes, deadlines, body)
	}
}

// TestForegroundEventStreamKeepaliveHasNoSequence 验证空闲 keepalive 是 comment，不消耗事件 sequence。
func TestForegroundEventStreamKeepaliveHasNoSequence(t *testing.T) {
	manager, execution, store, handle := streamTestFixture(t, "exec_stream_keepalive")
	if _, err := store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted}); err != nil {
		t.Fatalf("publish started: %v", err)
	}
	stream, _ := NewForegroundEventStream(context.Background(), manager, time.Second, 5*time.Millisecond)
	writer := newControlledStreamWriter()
	request, cancel := requestWithCancel()
	done := make(chan struct{})
	go func() { stream.Serve(writer, request, handle); close(done) }()
	eventually(t, time.Second, func() bool {
		body, _, _, _ := writer.snapshot()
		return strings.Contains(body, ": keepalive\n\n")
	})
	exitCode := 0
	_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
	_ = manager.Complete(execution.Descriptor().ID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after terminal")
	}
	cancel()
	body, _, _, _ := writer.snapshot()
	if strings.Contains(body, "id: 2\nevent: ") && !strings.Contains(body, "id: 2\nevent: exited") {
		t.Fatalf("keepalive consumed sequence: %q", body)
	}
}

// TestForegroundEventStreamWriteFailureCancelsExecution 验证慢写 deadline/断开错误触发前台取消。
func TestForegroundEventStreamWriteFailureCancelsExecution(t *testing.T) {
	manager, execution, store, handle := streamTestFixture(t, "exec_stream_write_failure")
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	stream, _ := NewForegroundEventStream(context.Background(), manager, time.Second, time.Minute)
	writer := newControlledStreamWriter()
	writer.writeErr = errors.New("write deadline exceeded")
	request, cancel := requestWithCancel()
	defer cancel()
	stream.Serve(writer, request, handle)
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationForegroundDisconnect {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestForegroundEventStreamRequestDisconnectCancelsExecution 验证 request context 断开由 coordinator 取消 execution。
func TestForegroundEventStreamRequestDisconnectCancelsExecution(t *testing.T) {
	manager, execution, _, handle := streamTestFixture(t, "exec_stream_disconnect")
	stream, _ := NewForegroundEventStream(context.Background(), manager, time.Second, time.Minute)
	writer := newControlledStreamWriter()
	request, disconnect := requestWithCancel()
	disconnect()
	stream.Serve(writer, request, handle)
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationForegroundDisconnect {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestForegroundEventStreamNeverBackpressuresPublisher 验证阻塞 writer 时 EventStore publisher 仍可持续排空输出。
func TestForegroundEventStreamNeverBackpressuresPublisher(t *testing.T) {
	manager, execution, store, handle := streamTestFixture(t, "exec_stream_backpressure")
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	writer := newControlledStreamWriter()
	writer.blockWrite = release
	writer.writeEntered = entered
	stream, _ := NewForegroundEventStream(context.Background(), manager, time.Second, time.Minute)
	request, cancel := requestWithCancel()
	defer cancel()
	done := make(chan struct{})
	go func() { stream.Serve(writer, request, handle); close(done) }()
	<-entered
	published := make(chan struct{})
	go func() {
		for index := 0; index < 100; index++ {
			_ = store.AppendOutput(context.Background(), RawOutputChunk{Stream: OutputStreamStdout, Data: []byte("x")})
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked behind SSE writer")
	}
	exitCode := 0
	_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
	_ = manager.Complete(execution.Descriptor().ID)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not finish")
	}
}

func streamTestFixture(t *testing.T, id ExecutionID) (*Manager, *Execution, *EventStore, *ExecutionHandle) {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	store, err := NewEventStore(id, systemClock{}, 1024)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	if err := manager.SetCancellationHandler(id, func(reason TerminationReason) error {
		if terminalExecutionState(execution.Descriptor().State) {
			return manager.Complete(id)
		}
		if err := execution.Transition(ExecutionCancelled, reason, nil); err != nil {
			return err
		}
		return manager.Complete(id)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	return manager, execution, store, &ExecutionHandle{ExecutionID: id, Events: store}
}

func publishStartedOutputExit(t *testing.T, execution *Execution, manager *Manager, store *EventStore) {
	t.Helper()
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	_ = store.AppendOutput(context.Background(), RawOutputChunk{Stream: OutputStreamStdout, Data: []byte("ok\n")})
	exitCode := 0
	_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
	_ = manager.Complete(execution.Descriptor().ID)
}

func durationPointer(value int64) *int64 { return &value }

func requestWithCancel() (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/executions", nil)
	return request, cancel
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}
