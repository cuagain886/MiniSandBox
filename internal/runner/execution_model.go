package runner

import (
	"errors"
	"sync"
	"time"

	"minisandbox/pkg/protocol"
)

// ExecutionID 是仅在当前 sandbox runner 内有意义的不可变 execution 标识。
type ExecutionID string

// ExecutionState 是 runner 内部状态机使用的状态，不直接作为 HTTP DTO。
type ExecutionState string

const (
	// ExecutionPending 表示请求已接受但用户进程尚未成功启动。
	ExecutionPending ExecutionState = "Pending"
	// ExecutionRunning 表示用户进程已经成功启动且尚未终止。
	ExecutionRunning ExecutionState = "Running"
	// ExecutionExited 表示主进程已经完成 wait；非零退出码同样属于此状态。
	ExecutionExited ExecutionState = "Exited"
	// ExecutionFailed 表示校验、启动或 runner 内部处理失败。
	ExecutionFailed ExecutionState = "Failed"
	// ExecutionCancelled 表示显式取消、前台断开或 runner 关闭赢得终态竞争。
	ExecutionCancelled ExecutionState = "Cancelled"
	// ExecutionTimedOut 表示 execution deadline 赢得终态竞争。
	ExecutionTimedOut ExecutionState = "TimedOut"
)

// TerminationReason 描述推动 execution 进入终态的内部原因，不直接暴露底层错误文本。
type TerminationReason string

const (
	// TerminationNone 表示 execution 尚未进入终态。
	TerminationNone TerminationReason = ""
	// TerminationValidationFailed 表示请求在进程启动前校验失败。
	TerminationValidationFailed TerminationReason = "validation_failed"
	// TerminationStartFailed 表示已校验命令无法成功启动。
	TerminationStartFailed TerminationReason = "start_failed"
	// TerminationProcessExited 表示主进程已产生可解释的退出状态。
	TerminationProcessExited TerminationReason = "process_exited"
	// TerminationExplicitCancel 表示控制面显式请求取消。
	TerminationExplicitCancel TerminationReason = "explicit_cancel"
	// TerminationForegroundDisconnect 表示前台请求连接消失。
	TerminationForegroundDisconnect TerminationReason = "foreground_disconnect"
	// TerminationDeadlineExceeded 表示 execution deadline 已到达。
	TerminationDeadlineExceeded TerminationReason = "deadline_exceeded"
	// TerminationRunnerShutdown 表示 runner 关闭触发执行清理。
	TerminationRunnerShutdown TerminationReason = "runner_shutdown"
	// TerminationInternalFailure 表示 pipe、wait 或 runner 内部不变量失败。
	TerminationInternalFailure TerminationReason = "internal_failure"
)

// ErrInvalidExecutionTransition 是非法状态边、终态重复写入或原因不匹配的稳定内部错误。
var ErrInvalidExecutionTransition = errors.New("invalid execution state transition")

// ExecutionDescriptor 是 execution 当前状态的只读快照；调用方修改其字段不会影响状态机。
type ExecutionDescriptor struct {
	// ID 是当前 execution 的稳定标识。
	ID ExecutionID
	// State 是快照生成时的内部状态。
	State ExecutionState
	// CreatedAt 是 factory 接受 execution 时取得的 UTC 时间。
	CreatedAt time.Time
	// TerminationReason 仅在终态中非空。
	TerminationReason TerminationReason
	// ExitCode 仅在 Exited 中存在，且允许为任意进程退出码。
	ExitCode *int
}

// Execution 保存单次执行的最小状态机；进程、输出和 manager 生命周期由后续组件负责。
type Execution struct {
	mu                sync.RWMutex
	id                ExecutionID
	state             ExecutionState
	createdAt         time.Time
	terminationReason TerminationReason
	exitCode          *int
}

func newPendingExecution(id ExecutionID, createdAt time.Time) *Execution {
	return &Execution{id: id, state: ExecutionPending, createdAt: createdAt}
}

// Descriptor 返回与内部存储解耦的当前状态快照。
func (e *Execution) Descriptor() ExecutionDescriptor {
	e.mu.RLock()
	defer e.mu.RUnlock()
	descriptor := ExecutionDescriptor{
		ID:                e.id,
		State:             e.state,
		CreatedAt:         e.createdAt,
		TerminationReason: e.terminationReason,
	}
	if e.exitCode != nil {
		exitCode := *e.exitCode
		descriptor.ExitCode = &exitCode
	}
	return descriptor
}

// Transition 原子校验并应用一条状态边；失败时 execution 保持原样。
func (e *Execution) Transition(next ExecutionState, reason TerminationReason, exitCode *int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !validExecutionTransition(e.state, next, reason, exitCode) {
		return ErrInvalidExecutionTransition
	}
	e.state = next
	e.terminationReason = reason
	if exitCode != nil {
		value := *exitCode
		e.exitCode = &value
	}
	return nil
}

// MapExecutionState 显式把内部状态转换为稳定 wire enum，未知内部值会被拒绝。
func MapExecutionState(state ExecutionState) (protocol.ExecutionState, error) {
	switch state {
	case ExecutionPending:
		return protocol.ExecutionStatePending, nil
	case ExecutionRunning:
		return protocol.ExecutionStateRunning, nil
	case ExecutionExited:
		return protocol.ExecutionStateExited, nil
	case ExecutionFailed:
		return protocol.ExecutionStateFailed, nil
	case ExecutionCancelled:
		return protocol.ExecutionStateCancelled, nil
	case ExecutionTimedOut:
		return protocol.ExecutionStateTimedOut, nil
	default:
		return "", errors.New("unknown internal execution state")
	}
}

func validExecutionTransition(current, next ExecutionState, reason TerminationReason, exitCode *int) bool {
	switch {
	case current == ExecutionPending && next == ExecutionRunning:
		return reason == TerminationNone && exitCode == nil
	case current == ExecutionPending && next == ExecutionFailed:
		return (reason == TerminationValidationFailed ||
			reason == TerminationStartFailed ||
			reason == TerminationInternalFailure) && exitCode == nil
	case current == ExecutionRunning && next == ExecutionExited:
		return reason == TerminationProcessExited && exitCode != nil
	case current == ExecutionRunning && next == ExecutionCancelled:
		return (reason == TerminationExplicitCancel ||
			reason == TerminationForegroundDisconnect ||
			reason == TerminationRunnerShutdown) && exitCode == nil
	case current == ExecutionRunning && next == ExecutionTimedOut:
		return reason == TerminationDeadlineExceeded && exitCode == nil
	case current == ExecutionRunning && next == ExecutionFailed:
		return reason == TerminationInternalFailure && exitCode == nil
	default:
		return false
	}
}
