package runner

import (
	"context"
	"errors"
	"sync"
	"time"

	"minisandbox/pkg/protocol"
)

// ErrInvalidTerminalCandidate 表示终止原因与 exit/error 字段组合不合法。
var ErrInvalidTerminalCandidate = errors.New("invalid execution terminal candidate")

// TerminalCandidate 是 wait、cancel、timeout、disconnect、shutdown 或内部错误提交的终态候选。
type TerminalCandidate struct {
	// Reason 是参与首胜裁决的内部终止原因。
	Reason TerminationReason
	// ExitCode 仅供 process_exited 使用，允许为零、非零或 signal 映射值。
	ExitCode *int
	// ErrorCode 仅供 failed terminal 使用，必须是稳定安全码。
	ErrorCode string
	// Message 仅供 failed terminal 使用，不得包含内部 cause、命令或秘密。
	Message string
	// Duration 是从成功启动到候选产生或收尾完成的非负时长。
	Duration time.Duration
}

// TerminalDecision 描述一次提交是否与已经锁定的赢家相同。
type TerminalDecision struct {
	// Won 表示本次原因是首个赢家，或与已锁定原因完全相同的幂等重复。
	Won bool
	// Winner 是当前已经锁定的唯一终止原因。
	Winner TerminationReason
}

type terminalRequest struct {
	candidate TerminalCandidate
	response  chan TerminalDecision
}

// TerminalArbiter 由单一 supervisor goroutine 串行选择终态，并等待进程与 pipe readers 收尾。
type TerminalArbiter struct {
	execution *Execution
	store     *EventStore
	finalized <-chan struct{}
	requests  chan terminalRequest
	done      chan struct{}

	mu       sync.RWMutex
	winner   *TerminalCandidate
	finalErr error
}

// NewTerminalArbiter 创建终态裁决器；finalized 关闭前不会改变 execution 状态或发布 terminal。
func NewTerminalArbiter(execution *Execution, store *EventStore, finalized <-chan struct{}) (*TerminalArbiter, error) {
	if execution == nil || store == nil || finalized == nil {
		return nil, errors.New("terminal arbiter is not configured")
	}
	arbiter := &TerminalArbiter{
		execution: execution,
		store:     store,
		finalized: finalized,
		requests:  make(chan terminalRequest),
		done:      make(chan struct{}),
	}
	go arbiter.run()
	return arbiter, nil
}

// Submit 提交终态候选；首个合法原因胜出，重复相同原因返回幂等成功，其他原因败选。
func (a *TerminalArbiter) Submit(ctx context.Context, candidate TerminalCandidate) (TerminalDecision, error) {
	if err := validateTerminalCandidate(candidate); err != nil {
		return TerminalDecision{}, err
	}
	if a == nil {
		return TerminalDecision{}, errors.New("terminal arbiter is unavailable")
	}
	candidate = cloneTerminalCandidate(candidate)
	response := make(chan TerminalDecision, 1)
	request := terminalRequest{candidate: candidate, response: response}
	select {
	case <-ctx.Done():
		return TerminalDecision{}, ctx.Err()
	case <-a.done:
		return a.decision(candidate.Reason), nil
	case a.requests <- request:
	}
	select {
	case <-ctx.Done():
		return TerminalDecision{}, ctx.Err()
	case decision := <-response:
		return decision, nil
	case <-a.done:
		return a.decision(candidate.Reason), nil
	}
}

// Wait 等待唯一 terminal 发布；内部状态或事件发布失败时返回稳定内部错误。
func (a *TerminalArbiter) Wait(ctx context.Context) error {
	if a == nil {
		return errors.New("terminal arbiter is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.finalErr
	}
}

func (a *TerminalArbiter) run() {
	defer close(a.done)
	finalized := false
	for {
		if finalized {
			a.mu.RLock()
			hasWinner := a.winner != nil
			a.mu.RUnlock()
			if hasWinner {
				a.publishWinner()
				return
			}
		}
		select {
		case <-a.finalized:
			finalized = true
			a.finalized = nil
		case request := <-a.requests:
			a.mu.Lock()
			if a.winner == nil {
				candidate := cloneTerminalCandidate(request.candidate)
				a.winner = &candidate
			}
			decision := TerminalDecision{
				Won:    a.winner.Reason == request.candidate.Reason,
				Winner: a.winner.Reason,
			}
			a.mu.Unlock()
			request.response <- decision
		}
	}
}

func (a *TerminalArbiter) publishWinner() {
	a.mu.RLock()
	winner := cloneTerminalCandidate(*a.winner)
	a.mu.RUnlock()
	next, event := terminalStateAndEvent(winner)
	if err := a.execution.Transition(next, winner.Reason, winner.ExitCode); err != nil {
		a.setFinalError(errors.New("apply execution terminal state failed"))
		return
	}
	if _, err := a.store.PublishControl(context.Background(), event); err != nil {
		a.setFinalError(errors.New("publish execution terminal event failed"))
	}
}

func (a *TerminalArbiter) setFinalError(err error) {
	a.mu.Lock()
	a.finalErr = err
	a.mu.Unlock()
}

func (a *TerminalArbiter) decision(reason TerminationReason) TerminalDecision {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.winner == nil {
		return TerminalDecision{}
	}
	return TerminalDecision{Won: a.winner.Reason == reason, Winner: a.winner.Reason}
}

func validateTerminalCandidate(candidate TerminalCandidate) error {
	if candidate.Duration < 0 {
		return ErrInvalidTerminalCandidate
	}
	switch candidate.Reason {
	case TerminationProcessExited:
		if candidate.ExitCode == nil || candidate.ErrorCode != "" || candidate.Message != "" {
			return ErrInvalidTerminalCandidate
		}
	case TerminationValidationFailed, TerminationStartFailed, TerminationInternalFailure:
		if candidate.ExitCode != nil || candidate.ErrorCode == "" || candidate.Message == "" {
			return ErrInvalidTerminalCandidate
		}
	case TerminationExplicitCancel, TerminationForegroundDisconnect, TerminationDeadlineExceeded, TerminationRunnerShutdown:
		if candidate.ExitCode != nil || candidate.ErrorCode != "" || candidate.Message != "" {
			return ErrInvalidTerminalCandidate
		}
	default:
		return ErrInvalidTerminalCandidate
	}
	return nil
}

func terminalStateAndEvent(candidate TerminalCandidate) (ExecutionState, protocol.ExecutionEvent) {
	durationMS := candidate.Duration.Milliseconds()
	event := protocol.ExecutionEvent{DurationMS: &durationMS}
	switch candidate.Reason {
	case TerminationProcessExited:
		event.Type = protocol.EventExited
		exitCode := *candidate.ExitCode
		event.ExitCode = &exitCode
		return ExecutionExited, event
	case TerminationDeadlineExceeded:
		event.Type = protocol.EventTimedOut
		return ExecutionTimedOut, event
	case TerminationExplicitCancel, TerminationForegroundDisconnect, TerminationRunnerShutdown:
		event.Type = protocol.EventCancelled
		return ExecutionCancelled, event
	default:
		event.Type = protocol.EventFailed
		event.ErrorCode = candidate.ErrorCode
		event.Message = candidate.Message
		return ExecutionFailed, event
	}
}

func cloneTerminalCandidate(candidate TerminalCandidate) TerminalCandidate {
	if candidate.ExitCode != nil {
		value := *candidate.ExitCode
		candidate.ExitCode = &value
	}
	return candidate
}
