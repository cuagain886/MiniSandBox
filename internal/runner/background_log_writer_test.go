package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestBackgroundLogWriterWritesOrderedLimitedTerminalLog 验证固定路径、0600、顺序、limit 和 terminal 完整落盘。
func TestBackgroundLogWriterWritesOrderedLimitedTerminalLog(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_background_log")
	execution, store := runningExecutionAndStoreWithID(t, id, 3)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, _ := NewTerminalArbiter(execution, store, finalized)
	writer, err := NewBackgroundLogWriter(directory, id, store, arbiter)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	_ = store.AppendOutput(context.Background(), RawOutputChunk{Stream: OutputStreamStdout, Data: []byte("hello")})
	exitCode := 0
	_, _ = arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode, Duration: time.Millisecond})
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	if err := writer.Wait(context.Background()); err != nil {
		t.Fatalf("wait writer: %v", err)
	}
	path, _ := BackgroundLogPath(directory, id)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("log metadata: info=%v err=%v", info, err)
	}
	events := readNDJSONEvents(t, path)
	if len(events) != 4 || events[0].Type != protocol.EventStarted || events[1].Type != protocol.EventStdout || events[2].Type != protocol.EventOutputLimitReached || events[3].Type != protocol.EventExited {
		t.Fatalf("events: %+v", events)
	}
	if events[1].Sequence != 2 || events[2].Sequence != 3 || events[3].Sequence != 4 || events[3].OutputTruncated == nil || !*events[3].OutputTruncated {
		t.Fatalf("sequence/truncation: %+v", events)
	}
}

// TestBackgroundLogWriterHandlesPartialWrites 验证合法 partial write 会循环直至每一行完整写入。
func TestBackgroundLogWriterHandlesPartialWrites(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_partial_log")
	execution, store := runningExecutionAndStoreWithID(t, id, 1024)
	defer store.Close()
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	exitCode := 0
	_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
	arbiter := completedUnusedArbiter(t, "exec_unused")
	file := &memoryLogFile{maxWrite: 3}
	writer, err := newBackgroundLogWriter(directory, id, store, arbiter, func(string, int, os.FileMode) (backgroundLogFile, error) { return file, nil })
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := writer.Wait(context.Background()); err != nil {
		t.Fatalf("wait writer: %v", err)
	}
	lines := readNDJSONFromBytes(t, file.data)
	if len(lines) != 2 || !lines[1].Terminal() || file.writeCalls < 2 || file.syncCalls != 1 || file.closeCalls != 1 {
		t.Fatalf("partial result: events=%+v writes=%d sync=%d close=%d", lines, file.writeCalls, file.syncCalls, file.closeCalls)
	}
}

// TestBackgroundLogWriterSubmitsFailureOnDiskError 验证写失败提交稳定 internal failure，不泄漏底层磁盘错误。
func TestBackgroundLogWriterSubmitsFailureOnDiskError(t *testing.T) {
	directory := t.TempDir()
	id := ExecutionID("exec_disk_failure")
	execution, store := runningExecutionAndStoreWithID(t, id, 1024)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, _ := NewTerminalArbiter(execution, store, finalized)
	file := &memoryLogFile{writeErr: errors.New("secret disk path")}
	writer, err := newBackgroundLogWriter(directory, id, store, arbiter, func(string, int, os.FileMode) (backgroundLogFile, error) { return file, nil })
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
	if err := writer.Wait(context.Background()); !errors.Is(err, ErrBackgroundLogWrite) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("writer error: %v", err)
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionFailed || got.TerminationReason != TerminationInternalFailure {
		t.Fatalf("descriptor: %+v", got)
	}
}

// TestBackgroundLogWriterReportsSyncAndCloseErrors 验证 terminal 后 sync/close 错误均受控返回。
func TestBackgroundLogWriterReportsSyncAndCloseErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		syncErr  error
		closeErr error
	}{
		{name: "sync", syncErr: errors.New("sync failed")},
		{name: "close", closeErr: errors.New("close failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			id := ExecutionID("exec_" + test.name + "_failure")
			execution, store := runningExecutionAndStoreWithID(t, id, 1024)
			defer store.Close()
			_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventStarted})
			exitCode := 0
			_ = execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode)
			_, _ = store.PublishControl(context.Background(), protocol.ExecutionEvent{Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: durationPointer(1)})
			arbiter := completedUnusedArbiter(t, ExecutionID("exec_unused_"+test.name))
			file := &memoryLogFile{syncErr: test.syncErr, closeErr: test.closeErr}
			writer, err := newBackgroundLogWriter(directory, id, store, arbiter, func(string, int, os.FileMode) (backgroundLogFile, error) { return file, nil })
			if err != nil {
				t.Fatalf("new writer: %v", err)
			}
			if err := writer.Wait(context.Background()); !errors.Is(err, ErrBackgroundLogWrite) {
				t.Fatalf("wait writer: %v", err)
			}
		})
	}
}

// TestBackgroundLogWriterRejectsTraversalAndPreexistingSymlink 验证 ID 不能影响目录，且 O_EXCL 拒绝预置 symlink。
func TestBackgroundLogWriterRejectsTraversalAndPreexistingSymlink(t *testing.T) {
	directory := t.TempDir()
	for _, id := range []ExecutionID{"../escape", "exec_a/b", "exec_a\\b", "exec_"} {
		if _, err := BackgroundLogPath(directory, id); !errors.Is(err, ErrBackgroundLogWrite) {
			t.Fatalf("unsafe ID accepted: %q err=%v", id, err)
		}
	}
	id := ExecutionID("exec_symlink_attack")
	path, _ := BackgroundLogPath(directory, id)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	execution, store := runningExecutionAndStoreWithID(t, id, 1024)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, _ := NewTerminalArbiter(execution, store, finalized)
	if _, err := NewBackgroundLogWriter(directory, id, store, arbiter); !errors.Is(err, ErrBackgroundLogWrite) {
		t.Fatalf("symlink accepted: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "sentinel" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

type memoryLogFile struct {
	mu         sync.Mutex
	data       []byte
	maxWrite   int
	writeErr   error
	syncErr    error
	closeErr   error
	writeCalls int
	syncCalls  int
	closeCalls int
}

func (f *memoryLogFile) Write(data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	count := len(data)
	if f.maxWrite > 0 && count > f.maxWrite {
		count = f.maxWrite
	}
	f.data = append(f.data, data[:count]...)
	return count, nil
}
func (f *memoryLogFile) Sync() error  { f.syncCalls++; return f.syncErr }
func (f *memoryLogFile) Close() error { f.closeCalls++; return f.closeErr }

func runningExecutionAndStoreWithID(t *testing.T, id ExecutionID, maxBytes int64) (*Execution, *EventStore) {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	store, err := NewEventStore(id, systemClock{}, maxBytes)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return execution, store
}

func newTestEventStore(t *testing.T, id ExecutionID) *EventStore {
	t.Helper()
	store, err := NewEventStore(id, systemClock{}, 1024)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func completedUnusedArbiter(t *testing.T, id ExecutionID) *TerminalArbiter {
	t.Helper()
	execution := newPendingExecution(id, time.Now())
	store := newTestEventStore(t, id)
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new unused arbiter: %v", err)
	}
	_, err = arbiter.Submit(context.Background(), TerminalCandidate{
		Reason:    TerminationStartFailed,
		ErrorCode: "START_FAILED",
		Message:   "execution could not start",
	})
	if err != nil {
		t.Fatalf("submit unused arbiter: %v", err)
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait unused arbiter: %v", err)
	}
	return arbiter
}

func readNDJSONEvents(t *testing.T, path string) []protocol.ExecutionEvent {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var events []protocol.ExecutionEvent
	for scanner.Scan() {
		var event protocol.ExecutionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return events
}

func readNDJSONFromBytes(t *testing.T, data []byte) []protocol.ExecutionEvent {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.ndjson")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return readNDJSONEvents(t, path)
}
