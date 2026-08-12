package store

import (
	"context"
	"time"
)

// RuntimeAnomalyResourceType 是异常涉及的受管资源类别；取值固定，不能承载 runtime 原始名称。
type RuntimeAnomalyResourceType string

const (
	// RuntimeAnomalySandboxBundle 表示异常跨越 sandbox 资源集合或无法安全定位到单项资源。
	RuntimeAnomalySandboxBundle RuntimeAnomalyResourceType = "sandbox_bundle"
	// RuntimeAnomalyMainContainer 表示异常位于 sandbox 主容器。
	RuntimeAnomalyMainContainer RuntimeAnomalyResourceType = "main_container"
	// RuntimeAnomalyEgressSidecar 表示异常位于 outbound egress sidecar。
	RuntimeAnomalyEgressSidecar RuntimeAnomalyResourceType = "egress_sidecar"
	// RuntimeAnomalyWorkspaceVolume 表示异常位于受管 workspace 卷。
	RuntimeAnomalyWorkspaceVolume RuntimeAnomalyResourceType = "workspace_volume"
	// RuntimeAnomalyRuntimeDirectory 表示异常位于 sandbox runtime 目录。
	RuntimeAnomalyRuntimeDirectory RuntimeAnomalyResourceType = "runtime_directory"
)

// RuntimeAnomalyClassification 是可持久化的稳定异常分类，不包含底层错误文本。
type RuntimeAnomalyClassification string

const (
	// RuntimeAnomalyIncompleteBundle 表示受管资源集合缺少恢复所需成员。
	RuntimeAnomalyIncompleteBundle RuntimeAnomalyClassification = "incomplete_bundle"
	// RuntimeAnomalyUnknownSchema 表示资源恢复 schema 不受当前二进制支持。
	RuntimeAnomalyUnknownSchema RuntimeAnomalyClassification = "unknown_schema"
	// RuntimeAnomalyIdentityMismatch 表示资源投影的 sandbox 身份相互矛盾。
	RuntimeAnomalyIdentityMismatch RuntimeAnomalyClassification = "identity_mismatch"
	// RuntimeAnomalySpecHashMismatch 表示 bundle 成员的安全规格摘要不一致。
	RuntimeAnomalySpecHashMismatch RuntimeAnomalyClassification = "spec_hash_mismatch"
	// RuntimeAnomalySecurityProfileMismatch 表示安全配置、协议或策略摘要不可信。
	RuntimeAnomalySecurityProfileMismatch RuntimeAnomalyClassification = "security_profile_mismatch"
	// RuntimeAnomalyNetworkNamespaceMismatch 表示主容器未加入其 egress sidecar 的网络命名空间。
	RuntimeAnomalyNetworkNamespaceMismatch RuntimeAnomalyClassification = "network_namespace_mismatch"
	// RuntimeAnomalyLeaseUntrusted 表示本地租约或恢复 manifest 无法作为可信事实使用。
	RuntimeAnomalyLeaseUntrusted RuntimeAnomalyClassification = "lease_untrusted"
	// RuntimeAnomalyDuplicateResource 表示同一逻辑角色发现多个受管资源。
	RuntimeAnomalyDuplicateResource RuntimeAnomalyClassification = "duplicate_resource"
)

// RuntimeAnomalyObservation 是一次仅含安全标识和摘要的异常观测。
type RuntimeAnomalyObservation struct {
	// ResourceKey 是规范 sandbox/角色键或由安全字段生成的摘要键。
	ResourceKey string
	// ResourceType 是固定资源类别。
	ResourceType RuntimeAnomalyResourceType
	// Classification 是固定异常分类。
	Classification RuntimeAnomalyClassification
	// SafeFingerprint 是对稳定分类字段计算的 lowercase SHA-256，不得包含 raw runtime 数据。
	SafeFingerprint string
	// ObservedAt 是本次完整 inventory 扫描的 UTC 时间。
	ObservedAt time.Time
}

// RuntimeAnomaly 是异常事实的持久化快照。
type RuntimeAnomaly struct {
	RuntimeAnomalyObservation
	// FirstSeenAt 是该资源键首次出现异常的 UTC 时间。
	FirstSeenAt time.Time
	// LastSeenAt 是该资源键最近一次出现异常的 UTC 时间。
	LastSeenAt time.Time
	// ObservationCount 是该资源键累计观测次数，达到上限后保持饱和。
	ObservationCount uint32
	// ResolvedAt 非空表示一次完整扫描确认该异常资源已经消失；再次观测会重新激活。
	ResolvedAt *time.Time
}

// RuntimeAnomalyRepository 定义恢复扫描写入和读取安全异常事实的独立端口。
type RuntimeAnomalyRepository interface {
	// ObserveRuntimeAnomaly 按 ResourceKey 幂等 upsert；重复观测更新末见时间、摘要和饱和计数。
	ObserveRuntimeAnomaly(context.Context, RuntimeAnomalyObservation) (RuntimeAnomaly, error)
	// ListActiveRuntimeAnomalies 返回未解决异常，按 ResourceKey 稳定排序。
	ListActiveRuntimeAnomalies(context.Context) ([]RuntimeAnomaly, error)
	// ResolveRuntimeAnomaliesNotObserved 只解决完整扫描未见且末次观测不晚于扫描起点的 active 异常。
	ResolveRuntimeAnomaliesNotObserved(context.Context, RuntimeAnomalyResolution) (int, error)
}

// RuntimeAnomalyResolution 描述一次完整 inventory 扫描结束后的异常收尾边界。
type RuntimeAnomalyResolution struct {
	// ActiveResourceKeys 是本轮仍观察到的异常事实键；调用方不得传入 raw runtime 标识。
	ActiveResourceKeys []string
	// ScanStartedAt 是扫描代际的 UTC 时间边界；更晚观测到的事实不能被旧扫描解决。
	ScanStartedAt time.Time
	// ResolvedAt 是完整扫描成功结束的 UTC 时间，必须不早于 ScanStartedAt。
	ResolvedAt time.Time
}
