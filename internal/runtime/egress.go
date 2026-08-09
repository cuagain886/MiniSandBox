package runtime

import (
	"context"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresspolicy"
)

// EgressRequest 是平台配置与 sandbox ID 派生出的 sidecar Ensure 输入；公共请求
// 不能构造该类型，也不能覆盖 deny CIDR、artifact、身份或资源上限。
type EgressRequest struct {
	// SandboxID 是 sidecar 所属 sandbox 的规范 UUID v4。
	SandboxID string
	// Image 是经过配置校验的精确 OCI digest reference。
	Image string
	// AdditionalDeniedCIDRs 是只增不减的运维 deny 项。
	AdditionalDeniedCIDRs []string
	// AnchorUID 是 sidecar Ready 后固定的非 root UID。
	AnchorUID uint32
	// AnchorGID 是 sidecar Ready 后固定的非 root GID。
	AnchorGID uint32
	// Limits 是 sidecar 固定的 CPU、内存和 PID 上限。
	Limits domain.ResourceLimits
	// ReadyTimeout 是写入 bootstrap 后等待 attestation 的最长时间。
	ReadyTimeout time.Duration
}

// EgressActual 是 Docker adapter 验证后的 sidecar 与共享 network 事实。
type EgressActual struct {
	// SandboxID 是该聚合资源所属 sandbox ID。
	SandboxID string
	// ContainerID 是 namespace anchor 的 Docker ID。
	ContainerID string
	// NetworkID 是服务级受管 bridge 的 Docker ID。
	NetworkID string
	// State 是 sidecar 的粗粒度运行状态。
	State ActualState
	// Policy 是由内置基线、实际 IPAM 与运维追加项生成的不可变策略。
	Policy egresspolicy.Policy
	// Attestation 是 Ready sidecar 的只读证明；非 Ready 时为零值。
	Attestation egressanchor.Attestation
}

// EgressRuntime 定义 Phase 2 outbound 聚合资源的最小 runtime port。
type EgressRuntime interface {
	// EnsureEgress 幂等确保全局 bridge 与当前 sandbox sidecar 已 Ready。
	EnsureEgress(context.Context, EgressRequest) (EgressActual, error)
	// InspectEgress 只读验证当前 sandbox sidecar、策略和 attestation。
	InspectEgress(context.Context, EgressRequest) (EgressActual, error)
	// CheckEgressForExecution 在每次新 execution 前比较 sidecar health 与 runner netns。
	CheckEgressForExecution(context.Context, EgressRequest, string) error
}

// ExecutionEgressGate 使用 runtime 内受信平台配置验证 outbound execution 隔离身份。
type ExecutionEgressGate interface {
	// CheckSandboxEgress 只读校验 sidecar、策略、主容器拓扑和 runner netns，不进行修复。
	CheckSandboxEgress(context.Context, string, string) error
}
