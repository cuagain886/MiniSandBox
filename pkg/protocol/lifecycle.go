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
	// SandboxStateRunning 表示 sandbox 已经可以执行命令。
	SandboxStateRunning SandboxState = "Running"
	// SandboxStateStopping 表示 sandbox 正在清理受管资源。
	SandboxStateStopping SandboxState = "Stopping"
	// SandboxStateTerminated 表示 sandbox 的受管资源已经清理完成。
	SandboxStateTerminated SandboxState = "Terminated"
	// SandboxStateFailed 表示当前生命周期操作失败。
	SandboxStateFailed SandboxState = "Failed"
)

// CreateSandboxRequest 是创建 sandbox 的公共请求模型。
type CreateSandboxRequest struct {
	// Image 是 sandbox 使用的容器镜像引用。
	Image string `json:"image"`
}

// Sandbox 是生命周期 API 返回的公共资源描述。
type Sandbox struct {
	// ID 是控制面生成的稳定 sandbox 标识。
	ID string `json:"id"`
	// State 是最近一次观测到的生命周期状态。
	State SandboxState `json:"state"`
	// Image 是创建 sandbox 时请求的镜像引用。
	Image string `json:"image"`
	// CreatedAt 是控制面接受创建请求的 UTC 时间。
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt 是可选的 TTL 到期时间。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// FailureReason 是失败状态的安全诊断信息，不得包含秘密。
	FailureReason string `json:"failure_reason,omitempty"`
}
