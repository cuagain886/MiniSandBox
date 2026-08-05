package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestTerminalArbiterWaitsForFinalization 验证赢家锁定后仍等待进程和 readers 收尾再发布 terminal。
func TestTerminalArbiterWaitsForFinalization(t *testing.T) {
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	exitCode := 0
	decision, err := arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode, Duration: time.Second})
	if err != nil || !decision.Won {
		t.Fatalf("submit winner: decision=%+v err=%v", decision, err)
	}
	if got := execution.Descriptor().State; got != ExecutionRunning {
		t.Fatalf("state before finalization: %q", got)
	}
	if events := store.Events(); len(events) != 1 || events[0].Type != protocol.EventStarted {
		t.Fatalf("terminal published before finalization: %+v", events)
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	assertSingleTerminalLast(t, execution, store, TerminationProcessExited)
}

// TestTerminalArbiterPublishesStartFailureWithoutStarted 验证 Pending 启动失败直接产生 sequence 1 failed terminal。
func TestTerminalArbiterPublishesStartFailureWithoutStarted(t *testing.T) {
	execution := newPendingExecution("exec_test", time.Now())
	store, err := NewEventStore("exec_test", fixedClock{value: time.Now()}, 1024)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	decision, err := arbiter.Submit(context.Background(), TerminalCandidate{
		Reason:    TerminationStartFailed,
		ErrorCode: "START_FAILED",
		Message:   "execution could not start",
	})
	if err != nil || !decision.Won {
		t.Fatalf("submit start failure: decision=%+v err=%v", decision, err)
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	events := store.Events()
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Type != protocol.EventFailed || execution.Descriptor().State != ExecutionFailed {
		t.Fatalf("start failure outcome: descriptor=%+v events=%+v", execution.Descriptor(), events)
	}
}

// TestTerminalArbiterPairwiseRaces 验证 wait、cancel、timeout、disconnect、shutdown 和内部错误两两竞争只有一个赢家。
func TestTerminalArbiterPairwiseRaces(t *testing.T) {
	candidates := []TerminalCandidate{
		exitedCandidate(3),
		{Reason: TerminationExplicitCancel, Duration: time.Second},
		{Reason: TerminationDeadlineExceeded, Duration: time.Second},
		{Reason: TerminationForegroundDisconnect, Duration: time.Second},
		{Reason: TerminationRunnerShutdown, Duration: time.Second},
		{Reason: TerminationInternalFailure, ErrorCode: "INTERNAL_ERROR", Message: "execution failed", Duration: time.Second},
	}
	for left := range candidates {
		for right := left + 1; right < len(candidates); right++ {
			execution, store := runningExecutionAndStore(t)
			finalized := make(chan struct{})
			arbiter, err := NewTerminalArbiter(execution, store, finalized)
			if err != nil {
				t.Fatalf("new arbiter: %v", err)
			}
			decisions := make(chan TerminalDecision, 2)
			var wait sync.WaitGroup
			for _, candidate := range []TerminalCandidate{candidates[left], candidates[right]} {
				wait.Add(1)
				go func(candidate TerminalCandidate) {
					defer wait.Done()
					decision, submitErr := arbiter.Submit(context.Background(), candidate)
					if submitErr != nil {
						t.Errorf("submit: %v", submitErr)
						return
					}
					decisions <- decision
				}(candidate)
			}
			wait.Wait()
			close(decisions)
			wins := 0
			var winner TerminationReason
			for decision := range decisions {
				if decision.Won {
					wins++
					winner = decision.Winner
				}
			}
			if wins != 1 {
				t.Fatalf("pair %d/%d wins: %d", left, right, wins)
			}
			close(finalized)
			if err := arbiter.Wait(context.Background()); err != nil {
				t.Fatalf("wait arbiter: %v", err)
			}
			assertSingleTerminalLast(t, execution, store, winner)
			store.Close()
		}
	}
}

// TestTerminalArbiterMakesRepeatedCauseIdempotentAndRejectsLateCause 验证重复 cancel 成功且 terminal 后 timeout 败选。
func TestTerminalArbiterMakesRepeatedCauseIdempotentAndRejectsLateCause(t *testing.T) {
	execution, store := runningExecutionAndStore(t)
	defer store.Close()
	finalized := make(chan struct{})
	arbiter, err := NewTerminalArbiter(execution, store, finalized)
	if err != nil {
		t.Fatalf("new arbiter: %v", err)
	}
	cancel := TerminalCandidate{Reason: TerminationExplicitCancel, Duration: time.Second}
	for index := 0; index < 2; index++ {
		decision, err := arbiter.Submit(context.Background(), cancel)
		if err != nil || !decision.Won || decision.Winner != TerminationExplicitCancel {
			t.Fatalf("cancel %d: decision=%+v err=%v", index, decision, err)
		}
	}
	close(finalized)
	if err := arbiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait arbiter: %v", err)
	}
	late, err := arbiter.Submit(context.Background(), TerminalCandidate{Reason: TerminationDeadlineExceeded, Duration: time.Second})
	if err != nil || late.Won || late.Winner != TerminationExplicitCancel {
		t.Fatalf("late timeout: decision=%+v err=%v", late, err)
	}
	assertSingleTerminalLast(t, execution, store, TerminationExplicitCancel)
}

func runningExecutionAndStore(t *testing.T) (*Execution, *EventStore) {
	t.Helper()
	execution := newPendingExecution("exec_test", time.Now())
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("prepare running: %v", err)
	}
	store := newStartedEventStore(t, 1024)
	return execution, store
}

func exitedCandidate(exitCode int) TerminalCandidate {
	return TerminalCandidate{Reason: TerminationProcessExited, ExitCode: &exitCode, Duration: time.Second}
}

func assertSingleTerminalLast(t *testing.T, execution *Execution, store *EventStore, winner TerminationReason) {
	t.Helper()
	events := store.Events()
	terminals := 0
	for index, event := range events {
		if event.Terminal() {
			terminals++
			if index != len(events)-1 {
				t.Fatalf("terminal is not last: %+v", events)
			}
		}
	}
	if terminals != 1 || execution.Descriptor().TerminationReason != winner {
		t.Fatalf("terminal outcome: descriptor=%+v events=%+v", execution.Descriptor(), events)
	}
}
