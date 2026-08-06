//go:build linux

package runner

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestExecutionTimeoutRemovesRealProcessGroup 验证真实 deadline 会清除忽略 TERM 的 leader 与后代，并发布 timed_out。
func TestExecutionTimeoutRemovesRealProcessGroup(t *testing.T) {
	builder := newCommandBuilder(func() (string, error) { return "/bin/sh", nil })
	spec, err := builder.Build(ValidatedRequest{Shell: `trap '' TERM; sleep 30 & wait`, Timeout: time.Minute}, os.TempDir(), nil)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	started, err := StartCommand(spec)
	if err != nil {
		t.Fatalf("start command: %v", err)
	}
	readers, err := StartPipeReaders(started.Stdout, started.Stderr)
	if err != nil {
		t.Fatalf("start pipe readers: %v", err)
	}
	go func() {
		for range readers.Chunks {
		}
	}()
	reaped := make(chan struct{})
	startedAt := time.Now()
	go func() {
		_ = WaitProcess(started.Command, readers.Results, startedAt, systemClock{})
		close(reaped)
	}()
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	arbiter, err := NewTerminalArbiter(execution, store, reaped)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	terminator, err := NewProcessGroupTerminator(started.PGID, 20*time.Millisecond, reaped)
	if err != nil {
		t.Fatalf("new terminator: %v", err)
	}
	timeout, err := NewExecutionTimeout(30*time.Millisecond, time.Minute, startedAt, arbiter, terminator)
	if err != nil {
		t.Fatalf("new timeout: %v", err)
	}
	if err := timeout.Wait(context.Background()); err != nil {
		t.Fatalf("wait timeout: %v", err)
	}
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	if err := syscall.Kill(-started.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group still exists: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionTimedOut || got.TerminationReason != TerminationDeadlineExceeded {
		t.Fatalf("descriptor: %+v", got)
	}
}
