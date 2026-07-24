package protocol

import "time"

// ExecuteRequest 是发送给 runnerd 的命令执行请求。
type ExecuteRequest struct {
	// Argv 是不经过 shell 解析的参数数组，与 Shell 必须二选一。
	Argv []string `json:"argv,omitempty"`
	// Shell 是显式请求 shell 解释的命令文本，与 Argv 必须二选一。
	Shell string `json:"shell,omitempty"`
	// Env 是本次用户命令的附加环境变量，runner 内部变量必须在合并前过滤。
	Env map[string]string `json:"env,omitempty"`
	// WorkingDir 是 sandbox 容器内的执行目录。
	WorkingDir string `json:"working_dir,omitempty"`
	// Timeout 是执行超时，Go JSON wire 值当前以纳秒表示。
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ExecuteAccepted 表示 runner 已接受并分配了可追踪的执行 ID。
type ExecuteAccepted struct {
	// ExecutionID 是执行流、取消请求和日志关联使用的稳定标识。
	ExecutionID string `json:"execution_id"`
}

// ExitResult 描述进程组终止后的确定结果。
type ExitResult struct {
	// ExitCode 是主进程退出码；被信号终止时由 runner 映射为稳定值。
	ExitCode int `json:"exit_code"`
	// TimedOut 表示执行因超时触发了进程组终止。
	TimedOut bool `json:"timed_out,omitempty"`
	// Canceled 表示执行因调用方显式取消而终止。
	Canceled bool `json:"canceled,omitempty"`
}
