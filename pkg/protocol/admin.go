package protocol

import "time"

// DiagnosticsSectionStatus 表示单个诊断数据源是否在本次有界快照中可用。
type DiagnosticsSectionStatus string

const (
	// DiagnosticsSectionAvailable 表示该 section 已在本次请求的独立时限内成功采集。
	DiagnosticsSectionAvailable DiagnosticsSectionStatus = "available"
	// DiagnosticsSectionUnavailable 表示该 section 未能安全采集，调用方只能读取固定不可用分类。
	DiagnosticsSectionUnavailable DiagnosticsSectionStatus = "unavailable"
)

// DiagnosticsUnavailableCode 是 section 不可用时允许公开的低基数安全分类。
type DiagnosticsUnavailableCode string

const (
	// DiagnosticsUnavailableTimeout 表示该 section 的独立采集时限已到。
	DiagnosticsUnavailableTimeout DiagnosticsUnavailableCode = "timeout"
	// DiagnosticsUnavailableDependency 表示该 section 依赖的本地组件不可用。
	DiagnosticsUnavailableDependency DiagnosticsUnavailableCode = "dependency_unavailable"
	// DiagnosticsUnavailableNotCollected 表示当前快照没有可证明的新鲜观测。
	DiagnosticsUnavailableNotCollected DiagnosticsUnavailableCode = "not_collected"
	// DiagnosticsUnavailableInternalError 表示采集失败无法进一步安全分类。
	DiagnosticsUnavailableInternalError DiagnosticsUnavailableCode = "internal_error"
)

// DiagnosticsDesiredState 是管理诊断允许公开的稳定期望状态。
type DiagnosticsDesiredState string

const (
	// DiagnosticsDesiredRunning 表示 Store 期望 sandbox 最终可运行。
	DiagnosticsDesiredRunning DiagnosticsDesiredState = "Running"
	// DiagnosticsDesiredTerminated 表示 Store 期望 sandbox 最终完成清理。
	DiagnosticsDesiredTerminated DiagnosticsDesiredState = "Terminated"
)

// DiagnosticsSandboxOrigin 表示 sandbox record 的受控来源分类。
type DiagnosticsSandboxOrigin string

const (
	// DiagnosticsOriginAPI 表示 record 来自公共 create API 或旧库回填。
	DiagnosticsOriginAPI DiagnosticsSandboxOrigin = "api"
	// DiagnosticsOriginRecoveredOrphan 表示 record 来自通过完整身份校验的可信孤儿资源。
	DiagnosticsOriginRecoveredOrphan DiagnosticsSandboxOrigin = "recovered_orphan"
)

// DiagnosticsComputeStatus 表示主容器或 egress sidecar 的清洗后运行状态。
type DiagnosticsComputeStatus string

const (
	// DiagnosticsComputeNotExpected 表示当前 resolved spec 不要求该计算资源。
	DiagnosticsComputeNotExpected DiagnosticsComputeStatus = "not_expected"
	// DiagnosticsComputeAbsent 表示完整盘点确认该计算资源不存在。
	DiagnosticsComputeAbsent DiagnosticsComputeStatus = "absent"
	// DiagnosticsComputeRunning 表示该计算资源存在且处于运行状态。
	DiagnosticsComputeRunning DiagnosticsComputeStatus = "running"
	// DiagnosticsComputeStopped 表示该计算资源存在但未运行。
	DiagnosticsComputeStopped DiagnosticsComputeStatus = "stopped"
	// DiagnosticsComputeUnknown 表示当前观测不足以安全判断资源状态。
	DiagnosticsComputeUnknown DiagnosticsComputeStatus = "unknown"
)

// DiagnosticsResourcePresence 表示非计算型受管资源是否存在。
type DiagnosticsResourcePresence string

const (
	// DiagnosticsResourceNotExpected 表示当前 resolved spec 不要求该资源。
	DiagnosticsResourceNotExpected DiagnosticsResourcePresence = "not_expected"
	// DiagnosticsResourceAbsent 表示完整盘点确认该资源不存在。
	DiagnosticsResourceAbsent DiagnosticsResourcePresence = "absent"
	// DiagnosticsResourcePresent 表示该资源存在且身份属于当前 sandbox。
	DiagnosticsResourcePresent DiagnosticsResourcePresence = "present"
	// DiagnosticsResourceUnknown 表示当前观测不足以安全判断资源是否存在。
	DiagnosticsResourceUnknown DiagnosticsResourcePresence = "unknown"
)

// DiagnosticsMatchStatus 表示实际资源与 Store 规格或安全基线的比较结果。
type DiagnosticsMatchStatus string

const (
	// DiagnosticsMatch 表示已检查字段全部匹配。
	DiagnosticsMatch DiagnosticsMatchStatus = "match"
	// DiagnosticsMismatch 表示至少一个已检查字段不匹配，但不会返回原始值。
	DiagnosticsMismatch DiagnosticsMatchStatus = "mismatch"
	// DiagnosticsMatchUnknown 表示当前观测不足以完成比较。
	DiagnosticsMatchUnknown DiagnosticsMatchStatus = "unknown"
)

// DiagnosticsRunnerHealth 是 runner 最近一次探测的固定健康分类。
type DiagnosticsRunnerHealth string

const (
	// DiagnosticsRunnerUnknown 表示尚无可证明的 runner 健康结果。
	DiagnosticsRunnerUnknown DiagnosticsRunnerHealth = "unknown"
	// DiagnosticsRunnerHealthy 表示最近一次 runner 探测成功。
	DiagnosticsRunnerHealthy DiagnosticsRunnerHealth = "healthy"
	// DiagnosticsRunnerDegraded 表示出现未达到自动恢复阈值的连续失败。
	DiagnosticsRunnerDegraded DiagnosticsRunnerHealth = "degraded"
	// DiagnosticsRunnerUnhealthy 表示 runner 已达到不健康阈值。
	DiagnosticsRunnerUnhealthy DiagnosticsRunnerHealth = "unhealthy"
	// DiagnosticsRunnerUnreachable 表示本次探测无法连接 runner。
	DiagnosticsRunnerUnreachable DiagnosticsRunnerHealth = "unreachable"
)

// DiagnosticsReconcileCode 是最近一次 reconcile 允许公开的安全结果分类。
type DiagnosticsReconcileCode string

const (
	// DiagnosticsReconcileNotRun 表示尚未记录一次完成的 reconcile。
	DiagnosticsReconcileNotRun DiagnosticsReconcileCode = "not_run"
	// DiagnosticsReconcileConverged 表示最近一次 reconcile 已收敛当前意图。
	DiagnosticsReconcileConverged DiagnosticsReconcileCode = "converged"
	// DiagnosticsReconcileRetryScheduled 表示失败已分类并持久化下一次重试。
	DiagnosticsReconcileRetryScheduled DiagnosticsReconcileCode = "retry_scheduled"
	// DiagnosticsReconcileCleanupPending 表示可信受管资源仍需继续清理。
	DiagnosticsReconcileCleanupPending DiagnosticsReconcileCode = "cleanup_pending"
	// DiagnosticsReconcileSpecDrift 表示规格或安全基线漂移，禁止自动覆盖。
	DiagnosticsReconcileSpecDrift DiagnosticsReconcileCode = "spec_drift"
	// DiagnosticsReconcileRuntimeUnavailable 表示本地 runtime 依赖暂时不可用。
	DiagnosticsReconcileRuntimeUnavailable DiagnosticsReconcileCode = "runtime_unavailable"
	// DiagnosticsReconcileInternalError 表示最近一次失败无法进一步安全分类。
	DiagnosticsReconcileInternalError DiagnosticsReconcileCode = "internal_error"
)

// DiagnosticsAnomalyClassification 是歧义 runtime 资源的固定安全分类。
type DiagnosticsAnomalyClassification string

const (
	// DiagnosticsAnomalyIncompleteBundle 表示受管资源 bundle 不完整。
	DiagnosticsAnomalyIncompleteBundle DiagnosticsAnomalyClassification = "incomplete_bundle"
	// DiagnosticsAnomalyUnknownSchema 表示资源使用无法识别的恢复 schema。
	DiagnosticsAnomalyUnknownSchema DiagnosticsAnomalyClassification = "unknown_schema"
	// DiagnosticsAnomalyIdentityMismatch 表示名称、label 或资源角色身份不一致。
	DiagnosticsAnomalyIdentityMismatch DiagnosticsAnomalyClassification = "identity_mismatch"
	// DiagnosticsAnomalySpecHashMismatch 表示安全 spec hash 比较失败。
	DiagnosticsAnomalySpecHashMismatch DiagnosticsAnomalyClassification = "spec_hash_mismatch"
	// DiagnosticsAnomalySecurityProfileMismatch 表示资源安全 profile 不符合基线。
	DiagnosticsAnomalySecurityProfileMismatch DiagnosticsAnomalyClassification = "security_profile_mismatch"
	// DiagnosticsAnomalyNetworkNamespaceMismatch 表示 outbound bundle 的网络命名空间关系不一致。
	DiagnosticsAnomalyNetworkNamespaceMismatch DiagnosticsAnomalyClassification = "network_namespace_mismatch"
	// DiagnosticsAnomalyLeaseUntrusted 表示租约投影缺失、过期或无法通过身份校验。
	DiagnosticsAnomalyLeaseUntrusted DiagnosticsAnomalyClassification = "lease_untrusted"
	// DiagnosticsAnomalyDuplicateResource 表示同一确定性角色出现多个候选资源。
	DiagnosticsAnomalyDuplicateResource DiagnosticsAnomalyClassification = "duplicate_resource"
)

// SandboxDiagnostics 是单个 sandbox 的一次有界、只读、清洗后诊断快照。
//
// 五个 section 都是必填；数据源失败时仍返回 status=unavailable 和固定分类，
// 不能省略 section 或回退到原始错误、Docker inspect、日志、路径与凭据。
type SandboxDiagnostics struct {
	// SandboxID 是被诊断的稳定 sandbox 标识。
	SandboxID string `json:"sandbox_id"`
	// GeneratedAt 是整个快照完成时的 UTC 时间，wire 使用 RFC3339Nano。
	GeneratedAt time.Time `json:"generated_at"`
	// Store 是权威生命周期记录的清洗摘要。
	Store StoreDiagnostics `json:"store"`
	// Runtime 是 Docker 聚合资源的 allowlist 摘要。
	Runtime RuntimeDiagnostics `json:"runtime"`
	// Runner 是最近一次 runner 健康观测的摘要。
	Runner RunnerDiagnostics `json:"runner"`
	// Reconcile 是最近一次完成的收敛结果摘要。
	Reconcile ReconcileDiagnostics `json:"reconcile"`
	// Anomaly 是与该 sandbox 关联的 active anomaly 聚合摘要。
	Anomaly AnomalyDiagnostics `json:"anomaly"`
}

// StoreDiagnostics 描述 Store 中的期望、观测、租约和重试元数据。
type StoreDiagnostics struct {
	// Status 表示该 section 是否成功采集。
	Status DiagnosticsSectionStatus `json:"status"`
	// UnavailableCode 在 Status=unavailable 时给出固定安全分类。
	UnavailableCode *DiagnosticsUnavailableCode `json:"unavailable_code,omitempty"`
	// DesiredState 是 Store 当前期望状态。
	DesiredState *DiagnosticsDesiredState `json:"desired_state,omitempty"`
	// ObservedState 是 Store 最近一次持久化的观测状态。
	ObservedState *SandboxState `json:"observed_state,omitempty"`
	// Reason 是与 ObservedState 配套的稳定 lifecycle reason。
	Reason *SandboxReason `json:"reason,omitempty"`
	// Revision 是 Store CAS 使用的单调修订号。
	Revision *uint64 `json:"revision,omitempty"`
	// ExpiresAt 是权威租约的 UTC 到期时间。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// RetryAttempt 是当前持久化重试序号；零值仍必须显式返回。
	RetryAttempt *uint32 `json:"retry_attempt,omitempty"`
	// NextReconcileAt 是已调度下一次收敛的 UTC 时间；未调度时省略。
	NextReconcileAt *time.Time `json:"next_reconcile_at,omitempty"`
	// LastReconcileAt 是最近一次完成收敛的 UTC 时间；尚未执行时省略。
	LastReconcileAt *time.Time `json:"last_reconcile_at,omitempty"`
	// HealthFailureCount 是 runner 连续失败计数；零值仍必须显式返回。
	HealthFailureCount *uint32 `json:"health_failure_count,omitempty"`
	// Origin 是 record 的受控来源分类。
	Origin *DiagnosticsSandboxOrigin `json:"origin,omitempty"`
}

// RuntimeDiagnostics 描述实际受管资源的存在性、状态和安全比较结果。
type RuntimeDiagnostics struct {
	// Status 表示该 section 是否成功采集。
	Status DiagnosticsSectionStatus `json:"status"`
	// UnavailableCode 在 Status=unavailable 时给出固定安全分类。
	UnavailableCode *DiagnosticsUnavailableCode `json:"unavailable_code,omitempty"`
	// MainContainer 是主容器的清洗后运行状态。
	MainContainer *DiagnosticsComputeStatus `json:"main_container,omitempty"`
	// EgressSidecar 是 egress sidecar 的清洗后运行状态。
	EgressSidecar *DiagnosticsComputeStatus `json:"egress_sidecar,omitempty"`
	// WorkspaceVolume 表示 workspace volume 是否存在。
	WorkspaceVolume *DiagnosticsResourcePresence `json:"workspace_volume,omitempty"`
	// RuntimeDirectory 表示受管 runtime directory 是否存在。
	RuntimeDirectory *DiagnosticsResourcePresence `json:"runtime_directory,omitempty"`
	// SecurityProfile 表示实际安全 profile 是否匹配固定基线。
	SecurityProfile *DiagnosticsMatchStatus `json:"security_profile,omitempty"`
	// SafeSpecHash 是 64 位小写十六进制 SHA-256，不包含原始 spec 字段。
	SafeSpecHash *string `json:"safe_spec_hash,omitempty"`
	// SpecHashMatch 表示实际资源身份 hash 是否与 Store 匹配。
	SpecHashMatch *DiagnosticsMatchStatus `json:"spec_hash_match,omitempty"`
}

// RunnerDiagnostics 描述 runner 最近一次安全健康观测。
type RunnerDiagnostics struct {
	// Status 表示该 section 是否成功采集。
	Status DiagnosticsSectionStatus `json:"status"`
	// UnavailableCode 在 Status=unavailable 时给出固定安全分类。
	UnavailableCode *DiagnosticsUnavailableCode `json:"unavailable_code,omitempty"`
	// Health 是最近一次探测的固定健康分类。
	Health *DiagnosticsRunnerHealth `json:"health,omitempty"`
	// LastCheckedAt 是最近一次完成探测的 UTC 时间；尚未探测时省略。
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}

// ReconcileDiagnostics 描述最近一次完成的收敛结果。
type ReconcileDiagnostics struct {
	// Status 表示该 section 是否成功采集。
	Status DiagnosticsSectionStatus `json:"status"`
	// UnavailableCode 在 Status=unavailable 时给出固定安全分类。
	UnavailableCode *DiagnosticsUnavailableCode `json:"unavailable_code,omitempty"`
	// LastCode 是最近一次结果的固定低基数安全分类。
	LastCode *DiagnosticsReconcileCode `json:"last_code,omitempty"`
	// LastFinishedAt 是最近一次收敛完成的 UTC 时间；尚未执行时省略。
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
}

// AnomalyDiagnostics 描述与 sandbox 关联的 active anomaly 聚合结果。
type AnomalyDiagnostics struct {
	// Status 表示该 section 是否成功采集。
	Status DiagnosticsSectionStatus `json:"status"`
	// UnavailableCode 在 Status=unavailable 时给出固定安全分类。
	UnavailableCode *DiagnosticsUnavailableCode `json:"unavailable_code,omitempty"`
	// ActiveCount 是当前未解决 anomaly 数量；零值仍必须显式返回。
	ActiveCount *uint64 `json:"active_count,omitempty"`
	// Classifications 是去重后的固定分类集合；指针使 available section 能显式返回空数组，
	// 同时让 unavailable section 省略该字段。集合不包含 resource key 或 fingerprint。
	Classifications *[]DiagnosticsAnomalyClassification `json:"classifications,omitempty"`
	// LastObservedAt 是最近一次 anomaly observation 的 UTC 时间；没有 active anomaly 时省略。
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
}
