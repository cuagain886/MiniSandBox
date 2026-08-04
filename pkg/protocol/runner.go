package protocol

import (
	"errors"
	"strconv"
	"strings"
)

// RunnerHealth 是 runnerd `/healthz` 返回的严格内部就绪证明。
type RunnerHealth struct {
	// Status 固定为 ok；其他值不能用于把 sandbox 推进到 Running。
	Status string `json:"status"`
	// Service 固定为 runnerd，防止把同一 socket 上的其他服务误判为 runner。
	Service string `json:"service"`
	// Version 是 runnerd 构建版本，仅用于诊断，不参与协议兼容放宽。
	Version string `json:"version"`
	// ProtocolVersion 必须与控制面及 Docker label 的整数版本精确相等。
	ProtocolVersion int `json:"protocol_version"`
	// NetNSIdentity 是当前进程 Linux network namespace 的 device/inode 身份。
	NetNSIdentity string `json:"netns_identity"`
}

// ValidateRunnerNetNSIdentity 校验 `linux-netns:<device>:<inode>` 的规范格式。
// device 和 inode 必须是非零十进制无符号整数，且编码不能含前导零。
func ValidateRunnerNetNSIdentity(value string) error {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "linux-netns" {
		return errors.New("runner netns identity format is invalid")
	}
	for _, part := range parts[1:] {
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != part {
			return errors.New("runner netns identity value is invalid")
		}
	}
	return nil
}

// ExecuteRequest 是发送给 runnerd 的命令执行请求。
type ExecuteRequest struct {
	// Argv 是不经过 shell 解析的参数数组，与 Shell 必须二选一。
	Argv []string `json:"argv,omitempty"`
	// Shell 是显式请求 shell 解释的命令文本，与 Argv 必须二选一。
	Shell string `json:"shell,omitempty"`
	// Env 是本次用户命令的附加环境变量，runner 内部变量必须在合并前过滤。
	Env map[string]string `json:"env,omitempty"`
	// Cwd 是 sandbox 容器内的执行目录；缺失时由 runner 使用 /workspace。
	Cwd string `json:"cwd,omitempty"`
	// TimeoutSeconds 是执行超时秒数；零表示使用服务端默认值。
	TimeoutSeconds int64 `json:"timeout_seconds,omitempty"`
	// Background 表示请求创建可通过 status、logs 和 cancel 管理的后台执行。
	Background bool `json:"background,omitempty"`
}

// ExecutionState 是后台 execution 查询接口返回的稳定状态枚举。
type ExecutionState string

const (
	// ExecutionStatePending 表示请求已接受但用户进程尚未成功启动。
	ExecutionStatePending ExecutionState = "Pending"
	// ExecutionStateRunning 表示用户进程已经启动且尚未产生终止事件。
	ExecutionStateRunning ExecutionState = "Running"
	// ExecutionStateExited 表示进程完成 wait；非零退出码也属于此状态。
	ExecutionStateExited ExecutionState = "Exited"
	// ExecutionStateFailed 表示执行校验、启动或 runner 内部处理失败。
	ExecutionStateFailed ExecutionState = "Failed"
	// ExecutionStateCancelled 表示显式取消、前台断开或 runner 关闭赢得终态竞争。
	ExecutionStateCancelled ExecutionState = "Cancelled"
	// ExecutionStateTimedOut 表示 execution deadline 赢得终态竞争。
	ExecutionStateTimedOut ExecutionState = "TimedOut"
)

// ExecutionDescriptor 是后台创建成功时返回的最小稳定描述符。
type ExecutionDescriptor struct {
	// ExecutionID 是 status、logs 和 cancel 操作使用的稳定标识。
	ExecutionID string `json:"execution_id"`
	// State 是创建响应时的 execution 状态，通常为 Pending 或 Running。
	State ExecutionState `json:"state"`
}

// ExecutionStatus 描述后台 execution 的当前状态和可选终止事件。
type ExecutionStatus struct {
	// ExecutionID 是被查询的稳定 execution 标识。
	ExecutionID string `json:"execution_id"`
	// State 是查询时最近一次观测到的 execution 状态。
	State ExecutionState `json:"state"`
	// TerminalEvent 仅在 execution 已终止时出现，并且必须是四种终止事件之一。
	TerminalEvent *ExecutionEvent `json:"terminal_event,omitempty"`
}

// ExecutionLogPage 是按事件 sequence 游标读取的一页后台日志。
type ExecutionLogPage struct {
	// Events 按 sequence 严格递增排列，且只包含 cursor 之后的完整事件。
	Events []ExecutionEvent `json:"events"`
	// NextCursor 是本页最后一个事件的 sequence；空页保持请求 cursor。
	NextCursor uint64 `json:"next_cursor"`
	// Complete 表示日志中已经包含唯一终止事件，不表示日志永久保留。
	Complete bool `json:"complete"`
}
