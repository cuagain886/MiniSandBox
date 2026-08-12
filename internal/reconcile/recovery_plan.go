package reconcile

import "minisandbox/internal/domain"

// RecoveryAction 是启动恢复计划允许的有限动作集合。
type RecoveryAction string

const (
	// RecoveryActionNoOp 表示事实已经稳定，不需要写入或唤醒。
	RecoveryActionNoOp RecoveryAction = "NO_OP"
	// RecoveryActionWake 表示把既有 Store 记录交给正常 reconciler 收敛。
	RecoveryActionWake RecoveryAction = "WAKE"
	// RecoveryActionRepairMetadata 表示先以 CAS 修复可信主容器的 runtime identity，再唤醒。
	RecoveryActionRepairMetadata RecoveryAction = "REPAIR_METADATA"
	// RecoveryActionImport 表示完整可信 orphan bundle 可以进入受限导入流程。
	RecoveryActionImport RecoveryAction = "IMPORT"
	// RecoveryActionAnomaly 表示必须隔离并报告，不能猜测、导入或改写。
	RecoveryActionAnomaly RecoveryAction = "ANOMALY"
)

const (
	recoveryPlanReasonStable                 = "STABLE"
	recoveryPlanReasonStoreNeedsReconcile    = "STORE_NEEDS_RECONCILE"
	recoveryPlanReasonTrustedOrphan          = "TRUSTED_ORPHAN"
	recoveryPlanReasonPartialOrphan          = "PARTIAL_ORPHAN"
	recoveryPlanReasonActualAnomaly          = "ACTUAL_ANOMALY"
	recoveryPlanReasonIdentityConflict       = "IDENTITY_CONFLICT"
	recoveryPlanReasonSpecDrift              = "SPEC_DRIFT"
	recoveryPlanReasonMetadataDrift          = "METADATA_DRIFT"
	recoveryPlanReasonTerminatedResurrection = "TERMINATED_RESURRECTION"
	recoveryPlanReasonStateInvalid           = "STATE_INVALID"
)

// RecoveryPlan 是纯计划函数的值结果，不持有 Store 或 Actual 的可变引用。
type RecoveryPlan struct {
	// SandboxID 是动作目标；双方均缺失时为空。
	SandboxID string
	// Action 是后续执行器唯一允许解释的动作。
	Action RecoveryAction
	// Reason 是不含原始资源细节的稳定分类。
	Reason string
	// ExpectedStoreRevision 是规划时读取的 revision，仅用于诊断陈旧计划；CAS 执行仍会重读最新记录。
	ExpectedStoreRevision uint64
	// ExpectedSpecHash 绑定规划时的 Store 规格，防止执行时覆盖已变化记录的 metadata。
	ExpectedSpecHash string
	// ExpectedRuntimeID 绑定规划时可信主容器 ID，防止复用陈旧 observation。
	ExpectedRuntimeID string
}

// PlanRecovery 将 Store 权威记录与 Actual 只读快照映射为单一 typed action。
// 该函数不执行 Store/runtime 操作，也绝不根据漂移事实重建或修改实际资源。
func PlanRecovery(stored *domain.Sandbox, actual *ActualResourceSnapshot) RecoveryPlan {
	plan := RecoveryPlan{Action: RecoveryActionNoOp, Reason: recoveryPlanReasonStable}
	if stored != nil {
		plan.SandboxID = stored.ID
		plan.ExpectedStoreRevision = stored.Revision
		plan.ExpectedSpecHash = stored.SpecHash
	} else if actual != nil {
		plan.SandboxID = actual.SandboxID
	}
	if actual != nil && actual.Main != nil {
		plan.ExpectedRuntimeID = actual.Main.ContainerID
	}
	if stored == nil && actual == nil {
		return plan
	}
	if stored == nil {
		if trustedImportBundle(actual) {
			plan.Action, plan.Reason = RecoveryActionImport, recoveryPlanReasonTrustedOrphan
		} else if len(actual.Anomalies) != 0 {
			plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonActualAnomaly
		} else {
			plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonPartialOrphan
		}
		return plan
	}
	if !validRecoveryStoredState(*stored) {
		plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonStateInvalid
		return plan
	}
	if actual == nil {
		if stored.DesiredState == domain.DesiredTerminated && stored.ObservedState == domain.StateTerminated {
			return plan
		}
		if stored.ObservedState == domain.StateTerminated {
			plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonTerminatedResurrection
			return plan
		}
		plan.Action, plan.Reason = RecoveryActionWake, recoveryPlanReasonStoreNeedsReconcile
		return plan
	}
	if stored.ID != actual.SandboxID {
		plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonIdentityConflict
		return plan
	}
	if len(actual.Anomalies) != 0 {
		plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonActualAnomaly
		return plan
	}
	if actualSpecDrift(*stored, actual) {
		plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonSpecDrift
		return plan
	}
	if stored.ObservedState == domain.StateTerminated && stored.DesiredState == domain.DesiredRunning {
		// Terminated 是不可逆观测；即使发现 Running 容器也只能报异常，不能借恢复流程复活记录。
		plan.Action, plan.Reason = RecoveryActionAnomaly, recoveryPlanReasonTerminatedResurrection
		return plan
	}
	if actual.Main != nil && stored.RuntimeID != actual.Main.ContainerID {
		plan.Action, plan.Reason = RecoveryActionRepairMetadata, recoveryPlanReasonMetadataDrift
		return plan
	}
	if stored.DesiredState == domain.DesiredTerminated {
		if actualHasResources(actual) || stored.ObservedState != domain.StateTerminated {
			plan.Action, plan.Reason = RecoveryActionWake, recoveryPlanReasonStoreNeedsReconcile
		}
		return plan
	}
	if stored.ObservedState == domain.StateRunning && trustedRunningBundle(actual) {
		return plan
	}
	plan.Action, plan.Reason = RecoveryActionWake, recoveryPlanReasonStoreNeedsReconcile
	return plan
}

func validRecoveryStoredState(sandbox domain.Sandbox) bool {
	if sandbox.DesiredState != domain.DesiredRunning && sandbox.DesiredState != domain.DesiredTerminated {
		return false
	}
	switch sandbox.ObservedState {
	case domain.StatePending, domain.StateCreating, domain.StateRunning, domain.StateStopping, domain.StateTerminated, domain.StateFailed:
		return true
	default:
		return false
	}
}

func trustedImportBundle(actual *ActualResourceSnapshot) bool {
	return trustedRunningBundle(actual) && (actual.Directory.Manifest != nil ||
		actual.Main.SchemaVersion == 2 && actual.Main.CreationExpiresAt != nil)
}

func trustedRunningBundle(actual *ActualResourceSnapshot) bool {
	return actual != nil && len(actual.Anomalies) == 0 && actual.Main != nil &&
		actual.Main.State == "Running" && actual.Workspace != nil && actual.Directory != nil &&
		actual.Directory.DirectoryPresent
}

func actualSpecDrift(stored domain.Sandbox, actual *ActualResourceSnapshot) bool {
	return actual.Main != nil && actual.Main.SpecHash != stored.SpecHash ||
		actual.Workspace != nil && actual.Workspace.SpecHash != stored.SpecHash ||
		actual.Directory != nil && actual.Directory.Manifest != nil && actual.Directory.Manifest.SpecHash != stored.SpecHash
}

func actualHasResources(actual *ActualResourceSnapshot) bool {
	return actual != nil && (actual.Main != nil || actual.Egress != nil || actual.Workspace != nil || actual.Directory != nil)
}
