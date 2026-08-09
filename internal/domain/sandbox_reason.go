package domain

const (
	// SandboxReasonCreateAccepted 表示创建意图已经持久化，只能用于 Pending。
	SandboxReasonCreateAccepted = "CREATE_ACCEPTED"
	// SandboxReasonCreatingRuntime 表示正在准备运行时资源，只能用于 Creating。
	SandboxReasonCreatingRuntime = "CREATING_RUNTIME"
	// SandboxReasonWaitingRunner 表示正在等待 runner 就绪，只能用于 Creating。
	SandboxReasonWaitingRunner = "WAITING_RUNNER"
	// SandboxReasonRunning 表示 sandbox 已经可以执行命令，只能用于 Running。
	SandboxReasonRunning = "RUNNING"
	// SandboxReasonDeleteAccepted 表示终止意图已经持久化，只能用于 Stopping。
	SandboxReasonDeleteAccepted = "DELETE_ACCEPTED"
	// SandboxReasonDeletingRuntime 表示正在删除运行时资源，只能用于 Stopping。
	SandboxReasonDeletingRuntime = "DELETING_RUNTIME"
	// SandboxReasonTerminated 表示受管资源已经确认不存在，只能用于 Terminated。
	SandboxReasonTerminated = "TERMINATED"
	// SandboxReasonImagePullFailed 表示镜像准备失败，只能用于 Failed。
	SandboxReasonImagePullFailed = "IMAGE_PULL_FAILED"
	// SandboxReasonArtifactInvalid 表示 runner/init 产物无效，只能用于 Failed。
	SandboxReasonArtifactInvalid = "ARTIFACT_INVALID"
	// SandboxReasonContainerCreateFailed 表示主容器创建失败，只能用于 Failed。
	SandboxReasonContainerCreateFailed = "CONTAINER_CREATE_FAILED"
	// SandboxReasonArtifactInjectionFailed 表示运行时产物注入失败，只能用于 Failed。
	SandboxReasonArtifactInjectionFailed = "ARTIFACT_INJECTION_FAILED"
	// SandboxReasonContainerStartFailed 表示主容器启动失败，只能用于 Failed。
	SandboxReasonContainerStartFailed = "CONTAINER_START_FAILED"
	// SandboxReasonRunnerUnhealthy 表示 runner 未通过启动健康检查，只能用于 Failed。
	SandboxReasonRunnerUnhealthy = "RUNNER_UNHEALTHY"
	// SandboxReasonRunnerProtocolMismatch 表示 runner 协议版本不兼容，只能用于 Failed。
	SandboxReasonRunnerProtocolMismatch = "RUNNER_PROTOCOL_MISMATCH"
	// SandboxReasonEgressUnhealthy 表示 outbound 隔离证明失效，只能用于 Failed。
	SandboxReasonEgressUnhealthy = "EGRESS_UNHEALTHY"
	// SandboxReasonSpecDrift 表示实际资源与持久化规格不一致，只能用于 Failed。
	SandboxReasonSpecDrift = "SPEC_DRIFT"
	// SandboxReasonCleanupPending 表示删除补偿尚未完成，可用于 Failed 或 Stopping。
	SandboxReasonCleanupPending = "CLEANUP_PENDING"
	// SandboxReasonRuntimeUnavailable 表示运行时依赖暂时不可用，只能用于 Failed。
	SandboxReasonRuntimeUnavailable = "RUNTIME_UNAVAILABLE"
	// SandboxReasonInternalError 表示无法安全分类的内部失败，只能用于 Failed。
	SandboxReasonInternalError = "INTERNAL_ERROR"
	// SandboxReasonRetryScheduled 表示已持久化下一次收敛时间，可用于 Creating、Stopping 或 Failed。
	SandboxReasonRetryScheduled = "RETRY_SCHEDULED"
	// SandboxReasonRecoveringRuntime 表示正在恢复丢失或停止的计算资源，只能用于 Creating。
	SandboxReasonRecoveringRuntime = "RECOVERING_RUNTIME"
	// SandboxReasonRunnerHealthDegraded 表示 runner 健康探测暂时降级但尚未越过恢复阈值，只能用于 Running。
	SandboxReasonRunnerHealthDegraded = "RUNNER_HEALTH_DEGRADED"
	// SandboxReasonTTLExpired 表示租约已经到期并进入删除收敛，只能用于 Stopping。
	SandboxReasonTTLExpired = "TTL_EXPIRED"
	// SandboxReasonOrphanImported 表示可信孤儿资源已导入 Store，可用于 Creating 或 Running。
	SandboxReasonOrphanImported = "ORPHAN_IMPORTED"
	// SandboxReasonOrphanExpired 表示导入时已经过期的可信孤儿正在删除，只能用于 Stopping。
	SandboxReasonOrphanExpired = "ORPHAN_EXPIRED"
)

// SandboxReasonStateAllowed 判断稳定 reason 是否允许与给定 observed state 组合。
//
// 调度 attempt 和 next reconcile time 不属于公共状态；未知 reason 或 state 必须返回 false，
// 防止内部字符串无意扩展公共协议。
func SandboxReasonStateAllowed(reason string, state SandboxState) bool {
	switch reason {
	case SandboxReasonCreateAccepted:
		return state == StatePending
	case SandboxReasonCreatingRuntime,
		SandboxReasonWaitingRunner,
		SandboxReasonRecoveringRuntime:
		return state == StateCreating
	case SandboxReasonRunning,
		SandboxReasonRunnerHealthDegraded:
		return state == StateRunning
	case SandboxReasonDeleteAccepted,
		SandboxReasonDeletingRuntime,
		SandboxReasonTTLExpired,
		SandboxReasonOrphanExpired:
		return state == StateStopping
	case SandboxReasonTerminated:
		return state == StateTerminated
	case SandboxReasonImagePullFailed,
		SandboxReasonArtifactInvalid,
		SandboxReasonContainerCreateFailed,
		SandboxReasonArtifactInjectionFailed,
		SandboxReasonContainerStartFailed,
		SandboxReasonRunnerUnhealthy,
		SandboxReasonRunnerProtocolMismatch,
		SandboxReasonEgressUnhealthy,
		SandboxReasonSpecDrift,
		SandboxReasonRuntimeUnavailable,
		SandboxReasonInternalError:
		return state == StateFailed
	case SandboxReasonCleanupPending:
		return state == StateFailed || state == StateStopping
	case SandboxReasonRetryScheduled:
		return state == StateCreating || state == StateStopping || state == StateFailed
	case SandboxReasonOrphanImported:
		return state == StateCreating || state == StateRunning
	default:
		return false
	}
}

// SandboxReasonPublicMessage 返回 reason 对应的固定安全公共文案。
//
// 返回 false 表示 reason 未冻结；调用方不得回退到 Store、runtime 或底层错误中的原始文本。
func SandboxReasonPublicMessage(reason string) (string, bool) {
	switch reason {
	case SandboxReasonCreateAccepted:
		return "Sandbox creation has been accepted.", true
	case SandboxReasonCreatingRuntime:
		return "Preparing sandbox runtime.", true
	case SandboxReasonWaitingRunner:
		return "Waiting for sandbox runner.", true
	case SandboxReasonRunning:
		return "Sandbox is running.", true
	case SandboxReasonDeleteAccepted:
		return "Sandbox deletion has been accepted.", true
	case SandboxReasonDeletingRuntime:
		return "Deleting sandbox runtime.", true
	case SandboxReasonTerminated:
		return "Sandbox runtime has been deleted.", true
	case SandboxReasonImagePullFailed:
		return "Failed to pull sandbox image.", true
	case SandboxReasonArtifactInvalid:
		return "Sandbox runtime artifacts are invalid.", true
	case SandboxReasonContainerCreateFailed:
		return "Failed to create sandbox container.", true
	case SandboxReasonArtifactInjectionFailed:
		return "Failed to inject sandbox runtime artifacts.", true
	case SandboxReasonContainerStartFailed:
		return "Failed to start sandbox container.", true
	case SandboxReasonRunnerUnhealthy:
		return "Sandbox runner is unhealthy.", true
	case SandboxReasonRunnerProtocolMismatch:
		return "Sandbox runner protocol is incompatible.", true
	case SandboxReasonEgressUnhealthy:
		return "Sandbox outbound isolation is unhealthy.", true
	case SandboxReasonSpecDrift:
		return "Sandbox runtime does not match the persisted specification.", true
	case SandboxReasonCleanupPending:
		return "Sandbox runtime cleanup is pending.", true
	case SandboxReasonRuntimeUnavailable:
		return "Sandbox runtime is temporarily unavailable.", true
	case SandboxReasonInternalError:
		return "An unexpected internal error occurred.", true
	case SandboxReasonRetryScheduled:
		return "Sandbox reconciliation retry is scheduled.", true
	case SandboxReasonRecoveringRuntime:
		return "Sandbox runtime is being recovered.", true
	case SandboxReasonRunnerHealthDegraded:
		return "Sandbox runner health is degraded.", true
	case SandboxReasonTTLExpired:
		return "Sandbox lease has expired.", true
	case SandboxReasonOrphanImported:
		return "Trusted sandbox resources have been imported.", true
	case SandboxReasonOrphanExpired:
		return "Expired sandbox resources are being deleted.", true
	default:
		return "", false
	}
}
