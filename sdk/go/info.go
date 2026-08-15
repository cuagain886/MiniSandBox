package sdk

import (
	"encoding/base64"
	"fmt"
	"time"

	"minisandbox/pkg/protocol"
)

// 本文件定义 SDK 面向调用方的信息与结果模型。与 wire model 不同，这些类型
// 使用 Go 原生时间类型并直接携带已解码的字节，调用方不再接触 RFC3339 细节、
// Base64 输出和指针形式的可选终止字段。

// SandboxInfo 是 SDK 交付给调用方的 sandbox 当前生命周期描述。
type SandboxInfo struct {
	// ID 是控制面生成的稳定 sandbox 标识。
	ID string
	// State 是最近一次观测到的生命周期状态。
	State SandboxState
	// Reason 是 State 对应的稳定机器可读原因。
	Reason SandboxReason
	// Message 是安全的人类可读状态说明。
	Message string
	// Image 是创建 sandbox 时请求的镜像引用。
	Image string
	// ExpiresAt 是当前租约的 UTC 到期时间。
	ExpiresAt time.Time
	// CreatedAt 是控制面接受创建请求的 UTC 时间。
	CreatedAt time.Time
	// UpdatedAt 是状态记录最近一次更新的 UTC 时间。
	UpdatedAt time.Time
}

// newSandboxInfo 把公共 wire sandbox 映射为 SDK 原生信息模型。
func newSandboxInfo(sandbox protocol.Sandbox) SandboxInfo {
	return SandboxInfo{
		ID:        sandbox.ID,
		State:     sandbox.State,
		Reason:    sandbox.Reason,
		Message:   sandbox.Message,
		Image:     sandbox.Image,
		ExpiresAt: sandbox.ExpiresAt,
		CreatedAt: sandbox.CreatedAt,
		UpdatedAt: sandbox.UpdatedAt,
	}
}

// ExecutionInfo 是 SDK 交付给调用方的后台 execution 当前状态描述。
type ExecutionInfo struct {
	// ExecutionID 是稳定 execution 标识。
	ExecutionID string
	// State 是查询时最近一次观测到的 execution 状态。
	State ExecutionState
	// TerminalEvent 仅在 execution 已终止时非 nil，是四种终止事件之一。
	TerminalEvent *DecodedEvent
}

// newExecutionInfo 把公共 wire 状态映射为 SDK 原生信息模型。
func newExecutionInfo(status protocol.ExecutionStatus) (ExecutionInfo, error) {
	info := ExecutionInfo{
		ExecutionID: status.ExecutionID,
		State:       status.State,
	}
	if status.TerminalEvent != nil {
		event, err := newDecodedEvent(*status.TerminalEvent)
		if err != nil {
			return ExecutionInfo{}, err
		}
		info.TerminalEvent = &event
	}
	return info, nil
}

// DecodedEvent 是 SDK 高层迭代器交付给调用方的单条已解码执行事件。
//
// 与 wire 别名 ExecutionEvent 不同，本类型把 Base64 输出还原为字节、把毫秒
// 耗时映射为 time.Duration、把可选终止字段扁平化：输出类事件的 Data 是原始
// stdout/stderr 字节；终止事件携带 ExitCode、Duration 和 OutputTruncated；
// ErrorCode 和 Message 仅出现在 failed 事件中。字段适用范围与公共协议一致，
// 由 Type 决定。
type DecodedEvent struct {
	// ExecutionID 是本事件所属的稳定 execution 标识。
	ExecutionID string
	// Sequence 是单次执行内从一开始单调递增的事件序号。
	Sequence uint64
	// Timestamp 是 runner 产生事件时的 UTC 时间。
	Timestamp time.Time
	// Type 决定其余字段的合法组合。
	Type EventType
	// Data 是已解码的输出字节；仅 stdout/stderr 事件非空。
	Data []byte
	// ExitCode 是进程退出码；仅 exited 事件携带，其余事件为零值。
	ExitCode int
	// Duration 是终止事件携带的执行耗时；非终止事件为零值。
	Duration time.Duration
	// OutputTruncated 是终止事件携带的输出截断标志；非终止事件为零值。
	OutputTruncated bool
	// ErrorCode 是稳定机器可读错误码；仅 failed 事件携带。
	ErrorCode string
	// Message 是安全的人类可读失败说明；仅 failed 事件携带。
	Message string
}

// Terminal 报告事件是否为四种互斥终止类型之一。
func (e DecodedEvent) Terminal() bool {
	switch e.Type {
	case EventExited, EventFailed, EventCancelled, EventTimedOut:
		return true
	default:
		return false
	}
}

// newDecodedEvent 把公共 wire 事件解码为 SDK 原生事件模型。
//
// wire 事件必须已经通过协议校验；本函数只负责把 Base64 输出还原为字节，
// 并把毫秒耗时映射为 time.Duration。
func newDecodedEvent(event protocol.ExecutionEvent) (DecodedEvent, error) {
	var data []byte
	if event.DataBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
		if err != nil {
			return DecodedEvent{}, fmt.Errorf(
				"minisandbox: decode %s event payload: %w", event.Type, err,
			)
		}
		data = decoded
	}
	result := DecodedEvent{
		ExecutionID: event.ExecutionID,
		Sequence:    event.Sequence,
		Timestamp:   event.Timestamp,
		Type:        event.Type,
		Data:        data,
		ErrorCode:   event.ErrorCode,
		Message:     event.Message,
	}
	if event.ExitCode != nil {
		result.ExitCode = *event.ExitCode
	}
	if event.DurationMS != nil {
		result.Duration = time.Duration(*event.DurationMS) * time.Millisecond
	}
	if event.OutputTruncated != nil {
		result.OutputTruncated = *event.OutputTruncated
	}
	return result, nil
}

// RunResult 是 Sandbox.Run 一次调用收集的完整执行结果。
type RunResult struct {
	// ExecutionID 是本次执行的稳定标识，可用于事后日志查询。
	ExecutionID string
	// State 是执行收敛到的终态。
	State ExecutionState
	// ExitCode 是进程退出码；仅 State 为 Exited 时有效，其余终态为 -1。
	ExitCode int
	// Stdout 是按事件顺序拼接的完整标准输出。
	Stdout []byte
	// Stderr 是按事件顺序拼接的完整标准错误。
	Stderr []byte
	// Duration 是终止事件携带的执行耗时。
	Duration time.Duration
	// OutputTruncated 表示输出预算耗尽导致 stdout/stderr 不完整。
	OutputTruncated bool
}
