package domain

// SandboxState 表示控制面最近一次观测到的 sandbox 生命周期状态。
type SandboxState string

const (
	// StatePending 表示创建请求已持久化，等待 reconciler 处理。
	StatePending SandboxState = "Pending"
	// StateCreating 表示 reconciler 正在创建资源或等待 runner 就绪。
	StateCreating SandboxState = "Creating"
	// StateRunning 表示容器运行且 runner 健康检查成功。
	StateRunning SandboxState = "Running"
	// StateStopping 表示删除意图已提交，受管资源正在清理。
	StateStopping SandboxState = "Stopping"
	// StateTerminated 表示全部受管运行时资源已确认不存在。
	StateTerminated SandboxState = "Terminated"
	// StateFailed 表示当前收敛失败，并记录了可诊断原因。
	StateFailed SandboxState = "Failed"
)

// DesiredState 表示控制面持久化的 sandbox 目标状态。
type DesiredState string

const (
	// DesiredRunning 要求 reconciler 保证 sandbox 可执行命令。
	DesiredRunning DesiredState = "Running"
	// DesiredTerminated 要求 reconciler 幂等清理全部受管资源。
	DesiredTerminated DesiredState = "Terminated"
)

// Terminal 判断观测状态是否无需等待进一步生命周期转换。
func (s SandboxState) Terminal() bool {
	return s == StateTerminated || s == StateFailed
}
