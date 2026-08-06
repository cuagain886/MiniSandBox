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

// TestForegroundDisconnectEventuallyRemovesProcessGroup 验证前台连接消失最终清除完整进程组，而非只结束 handler。
func TestForegroundDisconnectEventuallyRemovesProcessGroup(t *testing.T) {
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
		t.Fatalf("start readers: %v", err)
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
	execution := newPendingExecution("exec_real_disconnect", startedAt)
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	_, _ = manager.CreateExecution()
	_ = execution.Transition(ExecutionRunning, TerminationNone, nil)
	store := newStartedEventStore(t, 1024)
	defer store.Close()
	arbiter, err := NewTerminalArbiter(execution, store, reaped)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	terminator, err := NewProcessGroupTerminator(started.PGID, 20*time.Millisecond, reaped)
	if err != nil {
		t.Fatalf("new terminator: %v", err)
	}
	if err := manager.SetCancellationHandler(execution.Descriptor().ID, func(reason TerminationReason) error {
		decision, err := arbiter.Submit(context.Background(), TerminalCandidate{Reason: reason, Duration: time.Since(startedAt)})
		if err != nil {
			return err
		}
		if decision.Won {
			if err := terminator.Terminate(); err != nil {
				return err
			}
		}
		if err := arbiter.Wait(context.Background()); err != nil {
			return err
		}
		return manager.Complete(execution.Descriptor().ID)
	}); err != nil {
		t.Fatalf("set handler: %v", err)
	}
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	requestContext, disconnect := context.WithCancel(context.Background())
	coordinator, err := StartForegroundCoordinator(serverContext, requestContext, manager, execution.Descriptor().ID)
	if err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	disconnect()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coordinator.Wait(ctx); err != nil {
		t.Fatalf("wait coordinator: %v", err)
	}
	if err := syscall.Kill(-started.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group still exists: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationForegroundDisconnect {
		t.Fatalf("descriptor: %+v", got)
	}
}
