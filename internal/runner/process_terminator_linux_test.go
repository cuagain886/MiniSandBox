//go:build linux

package runner

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestProcessGroupTerminatorUsesNegativePGIDAndEscalates 验证只向负 PGID 发送 TERM、探测和 KILL，并等待 waiter。
func TestProcessGroupTerminatorUsesNegativePGIDAndEscalates(t *testing.T) {
	reaped := make(chan struct{})
	close(reaped)
	var mu sync.Mutex
	var calls []syscall.Signal
	probeCount := 0
	ops := processGroupTerminationOps{
		signal: func(target int, signal syscall.Signal) error {
			if target != -42 {
				t.Fatalf("signal target: got %d, want -42", target)
			}
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, signal)
			if signal == 0 {
				probeCount++
				if probeCount > 1 {
					return syscall.ESRCH
				}
			}
			return nil
		},
		after: immediateAfter,
		now:   time.Now,
	}
	terminator, err := newProcessGroupTerminator(42, time.Second, reaped, ops)
	if err != nil {
		t.Fatalf("new terminator: %v", err)
	}
	if err := terminator.Terminate(); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	want := []syscall.Signal{syscall.SIGTERM, 0, syscall.SIGKILL, 0}
	if len(calls) != len(want) {
		t.Fatalf("signals: got %v, want %v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("signals: got %v, want %v", calls, want)
		}
	}
}

// TestProcessGroupTerminatorHandlesGraceExitAlreadyExitedAndRepeat 验证 grace 内退出、ESRCH 和重复调用均不发送 KILL。
func TestProcessGroupTerminatorHandlesGraceExitAlreadyExitedAndRepeat(t *testing.T) {
	for _, termResult := range []error{nil, syscall.ESRCH} {
		calls := 0
		probe := 0
		terminator, err := newProcessGroupTerminator(42, time.Second, closedSignal(), processGroupTerminationOps{
			signal: func(_ int, signal syscall.Signal) error {
				calls++
				if signal == syscall.SIGTERM {
					return termResult
				}
				if signal == 0 {
					probe++
					return syscall.ESRCH
				}
				t.Fatalf("unexpected signal: %v", signal)
				return nil
			},
			after: immediateAfter,
			now:   time.Now,
		})
		if err != nil {
			t.Fatalf("new terminator: %v", err)
		}
		for index := 0; index < 2; index++ {
			if err := terminator.Terminate(); err != nil {
				t.Fatalf("terminate %d: %v", index, err)
			}
		}
		wantCalls := 1
		if termResult == nil {
			wantCalls = 2
		}
		if calls != wantCalls || (termResult == nil && probe != 1) {
			t.Fatalf("term result %v: calls=%d probe=%d", termResult, calls, probe)
		}
	}
}

// TestProcessGroupTerminatorRejectsUnsafePGIDAndSignalError 验证 0、1、负 PGID 及非 ESRCH signal 错误 fail closed。
func TestProcessGroupTerminatorRejectsUnsafePGIDAndSignalError(t *testing.T) {
	for _, pgid := range []int{-10, 0, 1} {
		if _, err := NewProcessGroupTerminator(pgid, time.Second, closedSignal()); err == nil {
			t.Fatalf("unsafe PGID accepted: %d", pgid)
		}
	}
	terminator, err := newProcessGroupTerminator(42, time.Second, closedSignal(), processGroupTerminationOps{
		signal: func(int, syscall.Signal) error { return syscall.EPERM },
		after:  immediateAfter,
		now:    time.Now,
	})
	if err != nil {
		t.Fatalf("new terminator: %v", err)
	}
	if err := terminator.Terminate(); !errors.Is(err, ErrProcessGroupTerminationFailed) {
		t.Fatalf("signal error: %v", err)
	}
}

// TestProcessGroupTerminatorKillsRealDescendantGroupAndPublishesOneCancelled 验证真实 leader/child 忽略 TERM 时升级 KILL，waiter 回收后只发布 cancelled。
func TestProcessGroupTerminatorKillsRealDescendantGroupAndPublishesOneCancelled(t *testing.T) {
	builder := newCommandBuilder(func() (string, error) { return "/bin/sh", nil })
	spec, err := builder.Build(ValidatedRequest{
		Shell:   `trap '' TERM; sleep 30 & wait`,
		Timeout: time.Minute,
	}, os.TempDir(), nil)
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
	go func() {
		_ = WaitProcess(started.Command, readers.Results, time.Now(), fixedClock{value: time.Now()})
		close(reaped)
	}()
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	arbiter, err := NewTerminalArbiter(execution, store, reaped)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	decision, err := arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationExplicitCancel, Duration: time.Second})
	if err != nil || !decision.Won {
		t.Fatalf("submit cancel: decision=%+v err=%v", decision, err)
	}
	terminator, err := NewProcessGroupTerminator(started.PGID, 20*time.Millisecond, reaped)
	if err != nil {
		t.Fatalf("new terminator: %v", err)
	}
	if err := terminator.Terminate(); err != nil {
		t.Fatalf("terminate process group: %v", err)
	}
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	if err := syscall.Kill(-started.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group still exists: %v", err)
	}
	events := store.Events()
	terminals := 0
	for _, event := range events {
		if event.Terminal() {
			terminals++
			if event.Type != protocol.EventCancelled {
				t.Fatalf("terminal type: %q", event.Type)
			}
		}
	}
	if terminals != 1 || execution.Descriptor().State != ExecutionCancelled {
		t.Fatalf("cancel outcome: descriptor=%+v events=%+v", execution.Descriptor(), events)
	}
}

func immediateAfter(time.Duration) <-chan time.Time {
	result := make(chan time.Time, 1)
	result <- time.Now()
	return result
}

func closedSignal() <-chan struct{} {
	result := make(chan struct{})
	close(result)
	return result
}
