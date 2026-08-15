package sdk

import "fmt"

// 本文件整理 SDK 的错误使用方式：调用方可以用 errors.As 直接判断 HTTP 层
// ResponseError，也可以用具体终态错误类型区分 Run 的非成功收敛结果，
// 不需要解析错误字符串。

// IsNotFound 报告服务端是否返回了稳定的 404 资源不存在错误。
func (e *ResponseError) IsNotFound() bool {
	return e.StatusCode == 404
}

// IsConflict 报告服务端是否返回了稳定的 409 状态冲突错误。
func (e *ResponseError) IsConflict() bool {
	return e.StatusCode == 409
}

// IsRetryable 报告保持请求不变并稍后重试是否可能成功。
func (e *ResponseError) IsRetryable() bool {
	return e.Detail.Retryable
}

// ExitError 表示用户进程完成 wait 但退出码非零。
//
// Sandbox.Run 在 State 收敛到 Exited 且 ExitCode 非零时返回该错误，同时
// 返回携带完整输出的 RunResult；调用方可用 errors.As 提取退出码。
type ExitError struct {
	// ExecutionID 是本次执行的稳定标识。
	ExecutionID string
	// ExitCode 是进程的原始非零退出码。
	ExitCode int
}

// Error 返回包含执行标识和退出码的诊断文本。
func (e *ExitError) Error() string {
	return fmt.Sprintf(
		"minisandbox: execution %s exited with code %d",
		e.ExecutionID,
		e.ExitCode,
	)
}

// ExecutionCancelledError 表示执行以 Cancelled 终态收敛。
type ExecutionCancelledError struct {
	// ExecutionID 是本次执行的稳定标识。
	ExecutionID string
}

// Error 返回包含执行标识的诊断文本。
func (e *ExecutionCancelledError) Error() string {
	return fmt.Sprintf("minisandbox: execution %s was cancelled", e.ExecutionID)
}

// ExecutionTimedOutError 表示执行以 TimedOut 终态收敛。
type ExecutionTimedOutError struct {
	// ExecutionID 是本次执行的稳定标识。
	ExecutionID string
}

// Error 返回包含执行标识的诊断文本。
func (e *ExecutionTimedOutError) Error() string {
	return fmt.Sprintf("minisandbox: execution %s timed out", e.ExecutionID)
}

// ExecutionFailedError 表示执行校验、启动或 runner 内部处理失败。
type ExecutionFailedError struct {
	// ExecutionID 是本次执行的稳定标识。
	ExecutionID string
	// ErrorCode 是 failed 终止事件携带的稳定机器可读错误码。
	ErrorCode string
	// Message 是 failed 终止事件携带的安全说明文本。
	Message string
}

// Error 返回包含执行标识、错误码和说明的诊断文本。
func (e *ExecutionFailedError) Error() string {
	return fmt.Sprintf(
		"minisandbox: execution %s failed: %s: %s",
		e.ExecutionID,
		e.ErrorCode,
		e.Message,
	)
}
