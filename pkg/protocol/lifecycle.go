// Package protocol 定义 MiniSandbox HTTP 和 SSE 的稳定 wire model。
//
// 本模块可被控制面、runnerclient、runner 和 SDK 共同依赖，不得引用 internal
// 包；协议字段和单位必须与 api 目录中的 OpenAPI 契约保持一致。
package protocol

import "time"

// SandboxState 是生命周期 API 对外返回的稳定状态枚举。
type SandboxState string

const (
	// SandboxStatePending 表示请求已接受，等待控制面处理。
	SandboxStatePending SandboxState = "Pending"
	// SandboxStateCreating 表示控制面正在创建资源或等待 runner。
	SandboxStateCreating SandboxState = "Creating"
	// SandboxStateRunning 表示容器运行且 runner 健康检查成功。
	SandboxStateRunning SandboxState = "Running"
	// SandboxStateStopping 表示 sandbox 正在清理受管资源。
	SandboxStateStopping SandboxState = "Stopping"
	// SandboxStateTerminated 表示 sandbox 的受管资源已经清理完成。
	SandboxStateTerminated SandboxState = "Terminated"
	// SandboxStateFailed 表示当前生命周期操作失败。
	SandboxStateFailed SandboxState = "Failed"
)

// SandboxReason 是对外稳定的生命周期状态原因。
type SandboxReason string

const (
	// SandboxReasonCreateAccepted 表示创建意图已经持久化。
	SandboxReasonCreateAccepted SandboxReason = "CREATE_ACCEPTED"
	// SandboxReasonCreatingRuntime 表示 reconciler 正在准备 Docker 资源。
	SandboxReasonCreatingRuntime SandboxReason = "CREATING_RUNTIME"
	// SandboxReasonWaitingRunner 表示容器已经启动，控制面正在等待 runner 就绪。
	SandboxReasonWaitingRunner SandboxReason = "WAITING_RUNNER"
	// SandboxReasonRunning 表示容器运行且 runner 健康检查成功。
	SandboxReasonRunning SandboxReason = "RUNNING"
	// SandboxReasonDeleteAccepted 表示删除意图已经持久化。
	SandboxReasonDeleteAccepted SandboxReason = "DELETE_ACCEPTED"
	// SandboxReasonDeletingRuntime 表示 reconciler 正在清理受管资源。
	SandboxReasonDeletingRuntime SandboxReason = "DELETING_RUNTIME"
	// SandboxReasonTerminated 表示受管资源已经确认不存在。
	SandboxReasonTerminated SandboxReason = "TERMINATED"
	// SandboxReasonImagePullFailed 表示容器镜像拉取失败。
	SandboxReasonImagePullFailed SandboxReason = "IMAGE_PULL_FAILED"
	// SandboxReasonArtifactInvalid 表示 runner 或 init 产物缺失或平台不匹配。
	SandboxReasonArtifactInvalid SandboxReason = "ARTIFACT_INVALID"
	// SandboxReasonContainerCreateFailed 表示 Docker 容器创建失败。
	SandboxReasonContainerCreateFailed SandboxReason = "CONTAINER_CREATE_FAILED"
	// SandboxReasonArtifactInjectionFailed 表示 runner 或 init 产物注入失败。
	SandboxReasonArtifactInjectionFailed SandboxReason = "ARTIFACT_INJECTION_FAILED"
	// SandboxReasonContainerStartFailed 表示 Docker 容器启动失败。
	SandboxReasonContainerStartFailed SandboxReason = "CONTAINER_START_FAILED"
	// SandboxReasonRunnerUnhealthy 表示 runner 未在规定时间内就绪。
	SandboxReasonRunnerUnhealthy SandboxReason = "RUNNER_UNHEALTHY"
	// SandboxReasonSpecDrift 表示已有容器与持久化的 sandbox 规格不一致。
	SandboxReasonSpecDrift SandboxReason = "SPEC_DRIFT"
	// SandboxReasonCleanupPending 表示创建失败后的资源补偿尚未完成。
	SandboxReasonCleanupPending SandboxReason = "CLEANUP_PENDING"
	// SandboxReasonRuntimeUnavailable 表示 Docker daemon 暂时不可用。
	SandboxReasonRuntimeUnavailable SandboxReason = "RUNTIME_UNAVAILABLE"
	// SandboxReasonInternalError 表示发生未分类错误，Message 必须使用安全固定文案。
	SandboxReasonInternalError SandboxReason = "INTERNAL_ERROR"
)

// CreateSandboxRequest 是创建 sandbox 的公共请求模型。
type CreateSandboxRequest struct {
	// Image 是 sandbox 使用的容器镜像引用。
	Image string `json:"image"`
	// Network 是可选网络请求；缺失时等价于 outbound=false。
	Network *SandboxNetworkRequest `json:"network,omitempty"`
}

// SandboxNetworkRequest 描述客户端唯一可选择的 sandbox 网络能力。
type SandboxNetworkRequest struct {
	// Outbound 表示是否请求受管公网出站能力；默认 false。
	Outbound bool `json:"outbound"`
}

// Sandbox 是生命周期 API 返回的公共资源描述。
type Sandbox struct {
	// ID 是控制面生成的稳定 sandbox 标识。
	ID string `json:"id"`
	// State 是最近一次观测到的生命周期状态。
	State SandboxState `json:"state"`
	// Reason 是 State 对应的稳定机器可读原因。
	Reason SandboxReason `json:"reason"`
	// Message 是安全的人类可读状态说明，不得包含秘密、宿主机路径或内部堆栈。
	Message string `json:"message"`
	// Image 是创建 sandbox 时请求的镜像引用。
	Image string `json:"image"`
	// CreatedAt 是控制面接受创建请求的 UTC 时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是状态记录最近一次更新的 UTC 时间。
	UpdatedAt time.Time `json:"updated_at"`
}
