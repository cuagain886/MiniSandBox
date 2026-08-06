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

// TestManagerShutdownKillsTermIgnoringProcessGroup 验证 runner shutdown 能升级 KILL 并清除忽略 TERM 的完整进程组。
func TestManagerShutdownKillsTermIgnoringProcessGroup(t *testing.T) {
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
	execution := newPendingExecution("exec_real_shutdown", startedAt)
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.CreateExecution(); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("start state: %v", err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := syscall.Kill(-started.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group still exists: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionCancelled || got.TerminationReason != TerminationRunnerShutdown {
		t.Fatalf("descriptor: %+v", got)
	}
}
