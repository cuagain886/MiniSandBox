package sdk

import "minisandbox/pkg/protocol"

// 本文件向调用方公开稳定的 sandbox 状态、execution 状态和事件类型常量，
// 使普通示例无需额外导入 minisandbox/pkg/protocol。
//
// 这些类型以别名形式与公共 wire model 保持同一 identity：SDK 值和 protocol 值
// 可以直接比较，协议枚举扩展时 SDK 自动跟随，不产生第二套需要同步的枚举。

// SandboxState 是生命周期 API 对外返回的稳定 sandbox 状态枚举。
type SandboxState = protocol.SandboxState

const (
	// SandboxStatePending 表示创建请求已接受，等待控制面处理。
	SandboxStatePending = protocol.SandboxStatePending
	// SandboxStateCreating 表示控制面正在创建资源或等待 runner。
	SandboxStateCreating = protocol.SandboxStateCreating
	// SandboxStateRunning 表示容器运行且 runner 健康检查成功。
	SandboxStateRunning = protocol.SandboxStateRunning
	// SandboxStateStopping 表示 sandbox 正在清理受管资源。
	SandboxStateStopping = protocol.SandboxStateStopping
	// SandboxStateTerminated 表示 sandbox 的受管资源已经清理完成。
	SandboxStateTerminated = protocol.SandboxStateTerminated
	// SandboxStateFailed 表示当前生命周期操作失败。
	SandboxStateFailed = protocol.SandboxStateFailed
)

// SandboxReason 是对外稳定的生命周期状态原因；取值是与公共协议一致的
// 机器可读字符串，调用方可按需与具体原因字面量比较。
type SandboxReason = protocol.SandboxReason

// ExecutionState 是后台 execution 查询接口返回的稳定状态枚举。
type ExecutionState = protocol.ExecutionState

const (
	// ExecutionStatePending 表示请求已接受但用户进程尚未成功启动。
	ExecutionStatePending = protocol.ExecutionStatePending
	// ExecutionStateRunning 表示用户进程已经启动且尚未产生终止事件。
	ExecutionStateRunning = protocol.ExecutionStateRunning
	// ExecutionStateExited 表示进程完成 wait；非零退出码也属于此状态。
	ExecutionStateExited = protocol.ExecutionStateExited
	// ExecutionStateFailed 表示执行校验、启动或 runner 内部处理失败。
	ExecutionStateFailed = protocol.ExecutionStateFailed
	// ExecutionStateCancelled 表示显式取消、前台断开或 runner 关闭赢得终态竞争。
	ExecutionStateCancelled = protocol.ExecutionStateCancelled
	// ExecutionStateTimedOut 表示 execution deadline 赢得终态竞争。
	ExecutionStateTimedOut = protocol.ExecutionStateTimedOut
)

// executionTerminalState 报告 execution 状态是否为四种互斥终态之一。
//
// SDK 的 Wait、CancelAndWait 和 Run 都以该判定收敛，不再由调用方枚举终态。
func executionTerminalState(state ExecutionState) bool {
	switch state {
	case ExecutionStateExited,
		ExecutionStateFailed,
		ExecutionStateCancelled,
		ExecutionStateTimedOut:
		return true
	default:
		return false
	}
}

// EventType 标识 SSE 执行事件的稳定类型。
type EventType = protocol.EventType

// ExecutionEvent 是公共 wire 执行事件的原样别名，保持既有底层 API 使用者
// 不经修改继续编译。
//
// 该类型与 GetExecutionLogs 返回的日志页元素、SSE data 载荷完全一致：
// 输出仍是 Base64 字符串，可选字段仍是指针形式。需要已解码输出、Go 原生
// 耗时和扁平化终止字段时，请使用高层迭代器交付的 DecodedEvent。
type ExecutionEvent = protocol.ExecutionEvent

const (
	// EventStarted 表示 runner 已成功启动用户进程。
	EventStarted = protocol.EventStarted
	// EventStdout 携带用户进程的标准输出片段。
	EventStdout = protocol.EventStdout
	// EventStderr 携带用户进程的标准错误片段。
	EventStderr = protocol.EventStderr
	// EventOutputLimitReached 表示输出预算首次耗尽，执行仍会继续排空输出。
	EventOutputLimitReached = protocol.EventOutputLimitReached
	// EventExited 表示进程完成 wait，包括非零退出码。
	EventExited = protocol.EventExited
	// EventFailed 表示执行校验、启动或 runner 内部处理失败。
	EventFailed = protocol.EventFailed
	// EventCancelled 表示显式取消、前台断开或 runner 关闭赢得终态竞争。
	EventCancelled = protocol.EventCancelled
	// EventTimedOut 表示 execution deadline 赢得终态竞争。
	EventTimedOut = protocol.EventTimedOut
)
