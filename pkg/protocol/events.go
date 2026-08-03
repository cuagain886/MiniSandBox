package protocol

import (
	"encoding/base64"
	"errors"
	"time"
)

// EventType 标识 SSE 执行事件的稳定类型。
type EventType string

const (
	// EventStarted 表示 runner 已成功启动用户进程。
	EventStarted EventType = "started"
	// EventStdout 携带用户进程的标准输出片段。
	EventStdout EventType = "stdout"
	// EventStderr 携带用户进程的标准错误片段。
	EventStderr EventType = "stderr"
	// EventOutputLimitReached 表示输出预算首次耗尽，执行仍会继续排空输出。
	EventOutputLimitReached EventType = "output_limit_reached"
	// EventExited 表示进程完成 wait，包括非零退出码。
	EventExited EventType = "exited"
	// EventFailed 表示执行校验、启动或 runner 内部处理失败。
	EventFailed EventType = "failed"
	// EventCancelled 表示显式取消、前台断开或 runner 关闭赢得终态竞争。
	EventCancelled EventType = "cancelled"
	// EventTimedOut 表示 execution deadline 赢得终态竞争。
	EventTimedOut EventType = "timed_out"
)

// ExecutionEvent 是 runner SSE 流中的单条有序事件。
type ExecutionEvent struct {
	// ExecutionID 是本事件所属的稳定 execution 标识。
	ExecutionID string `json:"execution_id"`
	// Sequence 是单次执行内从一开始单调递增的事件序号。
	Sequence uint64 `json:"sequence"`
	// Timestamp 是 runner 产生事件时的 UTC 时间。
	Timestamp time.Time `json:"timestamp"`
	// Type 决定其余可选字段的合法组合。
	Type EventType `json:"type"`
	// DataBase64 是 stdout/stderr 原始字节的标准 Base64 编码。
	DataBase64 string `json:"data_base64,omitempty"`
	// ExitCode 只允许出现在 exited 事件中；非零值仍表示正常进程终态。
	ExitCode *int `json:"exit_code,omitempty"`
	// DurationMS 是终止事件携带的执行耗时，单位为毫秒。
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// OutputTruncated 是终止事件携带的输出截断标志。
	OutputTruncated *bool `json:"output_truncated,omitempty"`
	// ErrorCode 只允许出现在 failed 事件中，且必须是稳定机器可读错误码。
	ErrorCode string `json:"error_code,omitempty"`
	// Message 只允许出现在 failed 事件中，且不得包含命令、秘密或内部 cause。
	Message string `json:"message,omitempty"`
}

var errInvalidExecutionEvent = errors.New("invalid execution event")

// Validate 检查单个事件的必填字段、Base64 和事件类型专属字段。
//
// 本方法只验证单条 wire event；跨事件 sequence 连续性与唯一终态由流式解码器
// 或事件存储负责验证。
func (e ExecutionEvent) Validate() error {
	if e.ExecutionID == "" || e.Sequence == 0 || e.Timestamp.IsZero() {
		return errInvalidExecutionEvent
	}
	hasTerminal := e.DurationMS != nil && e.OutputTruncated != nil
	hasNoTerminal := e.DurationMS == nil && e.OutputTruncated == nil
	hasFailure := e.ErrorCode != "" || e.Message != ""
	switch e.Type {
	case EventStarted, EventOutputLimitReached:
		if e.DataBase64 != "" || e.ExitCode != nil || !hasNoTerminal || hasFailure {
			return errInvalidExecutionEvent
		}
	case EventStdout, EventStderr:
		if e.DataBase64 == "" || e.ExitCode != nil || !hasNoTerminal || hasFailure {
			return errInvalidExecutionEvent
		}
		if _, err := base64.StdEncoding.DecodeString(e.DataBase64); err != nil {
			return errInvalidExecutionEvent
		}
	case EventExited:
		if e.DataBase64 != "" || e.ExitCode == nil || !hasTerminal || hasFailure {
			return errInvalidExecutionEvent
		}
	case EventFailed:
		if e.DataBase64 != "" || e.ExitCode != nil || !hasTerminal ||
			e.ErrorCode == "" || e.Message == "" {
			return errInvalidExecutionEvent
		}
	case EventCancelled, EventTimedOut:
		if e.DataBase64 != "" || e.ExitCode != nil || !hasTerminal || hasFailure {
			return errInvalidExecutionEvent
		}
	default:
		return errInvalidExecutionEvent
	}
	if e.DurationMS != nil && *e.DurationMS < 0 {
		return errInvalidExecutionEvent
	}
	return nil
}

// Terminal 报告事件是否为四种互斥终止类型之一。
func (e ExecutionEvent) Terminal() bool {
	switch e.Type {
	case EventExited, EventFailed, EventCancelled, EventTimedOut:
		return true
	default:
		return false
	}
}
