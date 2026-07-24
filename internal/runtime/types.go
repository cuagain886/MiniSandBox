package runtime

import "time"

// ActualState 表示容器运行时中资源的粗粒度实际状态。
type ActualState string

const (
	// ActualMissing 表示找不到受管容器。
	ActualMissing ActualState = "Missing"
	// ActualCreated 表示容器已创建但尚未运行。
	ActualCreated ActualState = "Created"
	// ActualRunning 表示容器进程正在运行。
	ActualRunning ActualState = "Running"
	// ActualStopped 表示容器存在但主进程已经退出。
	ActualStopped ActualState = "Stopped"
)

// ActualSandbox 汇总 reconciler 决策所需的 runtime 观测结果。
type ActualSandbox struct {
	ID           string
	ContainerID  string
	State        ActualState
	RunnerReady  bool
	DiscoveredAt time.Time
}
