package runner

import (
	"context"
	"sync"
	"testing"
	"time"
)

type manualTimeoutTimer struct {
	fired   chan time.Time
	mu      sync.Mutex
	stopped bool
}

func newManualTimeoutTimer() *manualTimeoutTimer {
	return &manualTimeoutTimer{fired: make(chan time.Time, 1)}
}

func (t *manualTimeoutTimer) C() <-chan time.Time { return t.fired }
func (t *manualTimeoutTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (t *manualTimeoutTimer) wasStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type recordingTerminator struct {
	mu        sync.Mutex
	calls     int
	finalized chan struct{}
	once      sync.Once
}

func (t *recordingTerminator) Terminate() error {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	if t.finalized != nil {
		t.once.Do(func() { close(t.finalized) })
	}
	return nil
}

func (t *recordingTerminator) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// TestExecutionTimeoutExpiresFromSuccessfulStart 验证 deadline 从成功启动时刻计时并发布 timed_out。
func TestExecutionTimeoutExpiresFromSuccessfulStart(t *testing.T) {
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	timer := newManualTimeoutTimer()
	startedAt := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	terminator := &recordingTerminator{finalized: finalized}
	timeout, err := newExecutionTimeout(time.Second, time.Minute, startedAt, fixedClock{value: startedAt.Add(3 * time.Second)}, func(duration time.Duration) timeoutTimer {
		if duration != time.Second {
			t.Fatalf("timer duration: %v", duration)
		}
		return timer
	}, arbiter, terminator)
	if err != nil {
		t.Fatalf("new timeout: %v", err)
	}
	timer.fired <- startedAt.Add(time.Second)
	if err := timeout.Wait(context.Background()); err != nil {
		t.Fatalf("wait timeout: %v", err)
	}
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	if got := execution.Descriptor(); got.State != ExecutionTimedOut || got.TerminationReason != TerminationDeadlineExceeded {
		t.Fatalf("descriptor: %+v", got)
	}
	if terminator.callCount() != 1 || !timer.wasStopped() {
		t.Fatalf("cleanup: terminator=%d timer_stopped=%v", terminator.callCount(), timer.wasStopped())
	}
}

// TestExecutionTimeoutStopsBeforeCompletion 验证其他终态完成前可停止 timer，且不会提交 timeout 或终止进程组。
func TestExecutionTimeoutStopsBeforeCompletion(t *testing.T) {
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	timer := newManualTimeoutTimer()
	terminator := &recordingTerminator{}
	timeout, err := newExecutionTimeout(time.Second, time.Minute, time.Now(), systemClock{}, func(time.Duration) timeoutTimer { return timer }, arbiter, terminator)
	if err != nil {
		t.Fatalf("new timeout: %v", err)
	}
	if err := timeout.Stop(); err != nil {
		t.Fatalf("stop timeout: %v", err)
	}
	if err := timeout.Stop(); err != nil {
		t.Fatalf("repeat stop timeout: %v", err)
	}
	if terminator.callCount() != 0 || !timer.wasStopped() || execution.Descriptor().State != ExecutionRunning {
		t.Fatalf("unexpected timeout cleanup: calls=%d stopped=%v state=%s", terminator.callCount(), timer.wasStopped(), execution.Descriptor().State)
	}
	exitCode := 0
	if _, err := arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode}); err != nil {
		t.Fatalf("submit exit: %v", err)
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
}

// TestExecutionTimeoutUsesDefaultAndReleasesTimer 验证零值请求采用默认值，退出路径总会停止底层 timer。
func TestExecutionTimeoutUsesDefaultAndReleasesTimer(t *testing.T) {
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	timer := newManualTimeoutTimer()
	timeout, err := newExecutionTimeout(0, 17*time.Second, time.Now(), systemClock{}, func(duration time.Duration) timeoutTimer {
		if duration != 17*time.Second {
			t.Fatalf("default duration: %v", duration)
		}
		return timer
	}, arbiter, &recordingTerminator{})
	if err != nil {
		t.Fatalf("new timeout: %v", err)
	}
	if err := timeout.Stop(); err != nil {
		t.Fatalf("stop timeout: %v", err)
	}
	if !timer.wasStopped() {
		t.Fatal("timer was not released")
	}
	exitCode := 0
	_, _ = arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode})
	close(finalized)
	_ = arbiter.Wait(context.Background())
}

// TestExecutionTimeoutAndCancelRace 验证 timeout/cancel 只能有一个赢家，败选 timeout 不得终止进程组。
func TestExecutionTimeoutAndCancelRace(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		execution, store := runningExecutionAndStore(t)
		finalized := make(chan struct{})
		arbiter, err := NewTerminalArbiter(execution, store, finalized)
		if err != nil {
			t.Fatalf("new arbiter: %v", err)
		}
		timer := newManualTimeoutTimer()
		terminator := &recordingTerminator{}
		timeout, err := newExecutionTimeout(time.Second, time.Minute, time.Now(), systemClock{}, func(time.Duration) timeoutTimer { return timer }, arbiter, terminator)
		if err != nil {
			t.Fatalf("new timeout: %v", err)
		}
		cancelDecision := make(chan TerminalDecision, 1)
		go func() {
			decision, submitErr := arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationExplicitCancel})
			if submitErr != nil {
				t.Errorf("submit cancel: %v", submitErr)
			}
			cancelDecision <- decision
		}()
		timer.fired <- time.Now()
		decision := <-cancelDecision
		if err := timeout.Wait(context.Background()); err != nil {
			t.Fatalf("wait timeout: %v", err)
		}
		close(finalized)
		if err := arbiter.Wait(context.Background()); err != nil {
			t.Fatalf("wait arbiter: %v", err)
		}
		if got := execution.Descriptor().TerminationReason; got != TerminationExplicitCancel && got != TerminationDeadlineExceeded {
			t.Fatalf("winner: %q", got)
		}
		if decision.Won && terminator.callCount() != 0 {
			t.Fatalf("losing timeout terminated group: %d", terminator.callCount())
		}
		if !decision.Won && terminator.callCount() != 1 {
			t.Fatalf("winning timeout did not terminate group: %d", terminator.callCount())
		}
		store.Close()
	}
}
