package protocol

// ReadinessStatus 是控制面或单个必要组件的公开就绪状态。
type ReadinessStatus string

const (
	// ReadinessStatusReady 表示当前对象已经满足服务请求的必要条件。
	ReadinessStatusReady ReadinessStatus = "ready"
	// ReadinessStatusNotReady 表示当前对象尚未满足服务请求的必要条件。
	ReadinessStatusNotReady ReadinessStatus = "not_ready"
)

// ReadinessComponentName 是 `/readyz` 允许公开的固定组件名称。
type ReadinessComponentName string

const (
	// ReadinessComponentStore 表示生命周期持久化存储。
	ReadinessComponentStore ReadinessComponentName = "store"
	// ReadinessComponentDocker 表示宿主机 Docker daemon 连接。
	ReadinessComponentDocker ReadinessComponentName = "docker"
	// ReadinessComponentArtifact 表示嵌入式 runner/init 产物。
	ReadinessComponentArtifact ReadinessComponentName = "artifact"
	// ReadinessComponentRecovery 表示启动恢复和首次资源对账。
	ReadinessComponentRecovery ReadinessComponentName = "recovery"
	// ReadinessComponentWorker 表示 reconcile worker。
	ReadinessComponentWorker ReadinessComponentName = "worker"
)

// ReadinessComponent 是单个必要组件的安全公开状态。
//
// 本模型不得增加错误 cause、宿主机路径、socket 地址或凭据字段。
type ReadinessComponent struct {
	// Name 是稳定的组件名称。
	Name ReadinessComponentName `json:"name"`
	// Status 只表示 ready 或 not_ready，不携带内部故障细节。
	Status ReadinessStatus `json:"status"`
}

// ReadinessResponse 是 `/readyz` 在成功和失败时共用的响应模型。
type ReadinessResponse struct {
	// Status 仅在所有 Components 均 ready 时为 ready。
	Status ReadinessStatus `json:"status"`
	// Components 按固定顺序列出全部必要组件，不省略未就绪项。
	Components []ReadinessComponent `json:"components"`
}
