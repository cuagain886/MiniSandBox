package runner

import (
	"errors"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionAllowsOnlyDocumentedEdges 逐条验证 Phase 2 状态图中的合法边。
func TestExecutionAllowsOnlyDocumentedEdges(t *testing.T) {
	exitZero, exitNonzero := 0, 17
	tests := []struct {
		name       string
		from       ExecutionState
		to         ExecutionState
		reason     TerminationReason
		exitCode   *int
		prepareRun bool
	}{
		{name: "pending to running", from: ExecutionPending, to: ExecutionRunning},
		{name: "pending validation failure", from: ExecutionPending, to: ExecutionFailed, reason: TerminationValidationFailed},
		{name: "pending start failure", from: ExecutionPending, to: ExecutionFailed, reason: TerminationStartFailed},
		{name: "pending internal failure", from: ExecutionPending, to: ExecutionFailed, reason: TerminationInternalFailure},
		{name: "running exited zero", from: ExecutionRunning, to: ExecutionExited, reason: TerminationProcessExited, exitCode: &exitZero, prepareRun: true},
		{name: "running exited nonzero", from: ExecutionRunning, to: ExecutionExited, reason: TerminationProcessExited, exitCode: &exitNonzero, prepareRun: true},
		{name: "running explicit cancel", from: ExecutionRunning, to: ExecutionCancelled, reason: TerminationExplicitCancel, prepareRun: true},
		{name: "running disconnect", from: ExecutionRunning, to: ExecutionCancelled, reason: TerminationForegroundDisconnect, prepareRun: true},
		{name: "running shutdown", from: ExecutionRunning, to: ExecutionCancelled, reason: TerminationRunnerShutdown, prepareRun: true},
		{name: "running timeout", from: ExecutionRunning, to: ExecutionTimedOut, reason: TerminationDeadlineExceeded, prepareRun: true},
		{name: "running internal failure", from: ExecutionRunning, to: ExecutionFailed, reason: TerminationInternalFailure, prepareRun: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := pendingExecution(t)
			if test.prepareRun {
				if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
					t.Fatalf("prepare running: %v", err)
				}
			}
			if got := execution.Descriptor().State; got != test.from {
				t.Fatalf("prepared state: got %q, want %q", got, test.from)
			}
			if err := execution.Transition(test.to, test.reason, test.exitCode); err != nil {
				t.Fatalf("legal transition rejected: %v", err)
			}
			got := execution.Descriptor()
			if got.State != test.to || got.TerminationReason != test.reason {
				t.Fatalf("descriptor: %+v", got)
			}
			if test.exitCode != nil && (got.ExitCode == nil || *got.ExitCode != *test.exitCode) {
				t.Fatalf("exit code: got %v, want %d", got.ExitCode, *test.exitCode)
			}
		})
	}
}

// TestExecutionRejectsIllegalEdgesWithoutMutation 覆盖所有状态组合，并确认非法转换不修改快照。
func TestExecutionRejectsIllegalEdgesWithoutMutation(t *testing.T) {
	states := []ExecutionState{ExecutionPending, ExecutionRunning, ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut}
	for _, from := range states {
		for _, to := range states {
			if (from == ExecutionPending && to == ExecutionRunning) || (from == ExecutionRunning && to == ExecutionExited) {
				continue
			}
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				execution := executionInState(t, from)
				before := execution.Descriptor()
				exitCode := 1
				err := execution.Transition(to, TerminationProcessExited, &exitCode)
				if !errors.Is(err, ErrInvalidExecutionTransition) {
					t.Fatalf("error: got %v, want stable transition error", err)
				}
				assertDescriptorEqual(t, execution.Descriptor(), before)
			})
		}
	}
}

// TestExecutionRejectsReasonMismatchAndRepeatedTerminal 验证合法状态边也不能携带错误原因，且终态不可逆。
func TestExecutionRejectsReasonMismatchAndRepeatedTerminal(t *testing.T) {
	execution := pendingExecution(t)
	before := execution.Descriptor()
	if err := execution.Transition(ExecutionRunning, TerminationExplicitCancel, nil); !errors.Is(err, ErrInvalidExecutionTransition) {
		t.Fatalf("running reason mismatch: %v", err)
	}
	assertDescriptorEqual(t, execution.Descriptor(), before)
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	exitCode := 9
	if err := execution.Transition(ExecutionExited, TerminationProcessExited, &exitCode); err != nil {
		t.Fatalf("exit: %v", err)
	}
	terminal := execution.Descriptor()
	for _, next := range []ExecutionState{ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut, ExecutionRunning} {
		if err := execution.Transition(next, TerminationInternalFailure, nil); !errors.Is(err, ErrInvalidExecutionTransition) {
			t.Fatalf("terminal transition to %q: %v", next, err)
		}
		assertDescriptorEqual(t, execution.Descriptor(), terminal)
	}
}

// TestMapExecutionStateUsesExplicitWireMapping 验证内部状态不会依赖字符串强转泄漏到协议层。
func TestMapExecutionStateUsesExplicitWireMapping(t *testing.T) {
	tests := map[ExecutionState]protocol.ExecutionState{
		ExecutionPending: protocol.ExecutionStatePending, ExecutionRunning: protocol.ExecutionStateRunning,
		ExecutionExited: protocol.ExecutionStateExited, ExecutionFailed: protocol.ExecutionStateFailed,
		ExecutionCancelled: protocol.ExecutionStateCancelled, ExecutionTimedOut: protocol.ExecutionStateTimedOut,
	}
	for internal, want := range tests {
		got, err := MapExecutionState(internal)
		if err != nil || got != want {
			t.Fatalf("map %q: got %q, err %v", internal, got, err)
		}
	}
	if _, err := MapExecutionState("unknown"); err == nil {
		t.Fatal("unknown internal state accepted")
	}
}

func pendingExecution(t *testing.T) *Execution {
	t.Helper()
	execution, err := NewPendingExecution("exec_test")
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	return execution
}

func executionInState(t *testing.T, state ExecutionState) *Execution {
	t.Helper()
	execution := pendingExecution(t)
	if state == ExecutionPending {
		return execution
	}
	if err := execution.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
		t.Fatalf("prepare running: %v", err)
	}
	if state == ExecutionRunning {
		return execution
	}
	exitCode := 0
	transitions := map[ExecutionState]struct {
		reason TerminationReason
		exit   *int
	}{
		ExecutionExited:    {TerminationProcessExited, &exitCode},
		ExecutionFailed:    {TerminationInternalFailure, nil},
		ExecutionCancelled: {TerminationExplicitCancel, nil},
		ExecutionTimedOut:  {TerminationDeadlineExceeded, nil},
	}
	transition := transitions[state]
	if err := execution.Transition(state, transition.reason, transition.exit); err != nil {
		t.Fatalf("prepare %q: %v", state, err)
	}
	return execution
}

func assertDescriptorEqual(t *testing.T, got, want ExecutionDescriptor) {
	t.Helper()
	if got.ID != want.ID || got.State != want.State || got.TerminationReason != want.TerminationReason {
		t.Fatalf("descriptor changed: got %+v, want %+v", got, want)
	}
	if (got.ExitCode == nil) != (want.ExitCode == nil) || (got.ExitCode != nil && *got.ExitCode != *want.ExitCode) {
		t.Fatalf("exit code changed: got %v, want %v", got.ExitCode, want.ExitCode)
	}
}
