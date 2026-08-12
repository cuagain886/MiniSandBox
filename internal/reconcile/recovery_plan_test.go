package reconcile

import (
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// TestPlanRecoveryCoversStoreActualMatrix 验证双方存在性、目标态和全部观测态组合的唯一动作。
func TestPlanRecoveryCoversStoreActualMatrix(t *testing.T) {
	states := []domain.SandboxState{domain.StatePending, domain.StateCreating, domain.StateRunning, domain.StateStopping, domain.StateTerminated, domain.StateFailed}
	for _, desired := range []domain.DesiredState{domain.DesiredRunning, domain.DesiredTerminated} {
		for _, state := range states {
			t.Run(string(desired)+"/"+string(state), func(t *testing.T) {
				stored := recoveryPlanSandbox(desired, state)
				actual := recoveryPlanActual()
				plan := PlanRecovery(&stored, &actual)
				want := RecoveryActionWake
				if desired == domain.DesiredRunning && state == domain.StateRunning {
					want = RecoveryActionNoOp
				}
				if desired == domain.DesiredRunning && state == domain.StateTerminated {
					want = RecoveryActionRecordAnomaly
				}
				if plan.Action != want {
					t.Fatalf("plan: %#v, want %s", plan, want)
				}
			})
		}
	}

	if got := PlanRecovery(nil, nil); got.Action != RecoveryActionNoOp {
		t.Fatalf("empty: %#v", got)
	}
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StatePending)
	if got := PlanRecovery(&stored, nil); got.Action != RecoveryActionWake {
		t.Fatalf("store only: %#v", got)
	}
	stored = recoveryPlanSandbox(domain.DesiredTerminated, domain.StateTerminated)
	if got := PlanRecovery(&stored, nil); got.Action != RecoveryActionNoOp {
		t.Fatalf("terminal store only: %#v", got)
	}
}

// TestPlanRecoveryImportsOnlyCompleteTrustedOrphan 验证 partial、未知 schema 与显式 anomaly 均不能导入。
func TestPlanRecoveryImportsOnlyCompleteTrustedOrphan(t *testing.T) {
	complete := recoveryPlanActual()
	if got := PlanRecovery(nil, &complete); got.Action != RecoveryActionImport {
		t.Fatalf("trusted orphan: %#v", got)
	}
	partial := complete
	partial.Workspace = nil
	if got := PlanRecovery(nil, &partial); got.Action != RecoveryActionRecordAnomaly || got.Reason != recoveryPlanReasonPartialOrphan {
		t.Fatalf("partial orphan: %#v", got)
	}
	unknownSchema := complete
	unknownSchema.Anomalies = []ActualAnomaly{{Code: ActualAnomalyResourceDamaged, Detail: runtimeport.DiscoverySchemaUnsupported}}
	if got := PlanRecovery(nil, &unknownSchema); got.Action != RecoveryActionRecordAnomaly || got.Reason != recoveryPlanReasonActualAnomaly {
		t.Fatalf("unknown schema: %#v", got)
	}
}

// TestPlanRecoveryRepairsMetadataBeforeWakeAndNeverOverlooksDrift 验证 identity 修复优先且 spec 漂移永不写入。
func TestPlanRecoveryRepairsMetadataBeforeWakeAndNeverOverlooksDrift(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredTerminated, domain.StateStopping)
	stored.RuntimeID = "old"
	actual := recoveryPlanActual()
	if got := PlanRecovery(&stored, &actual); got.Action != RecoveryActionRepairMetadata {
		t.Fatalf("metadata: %#v", got)
	}
	stored.SpecHash = strings.Repeat("b", 64)
	if got := PlanRecovery(&stored, &actual); got.Action != RecoveryActionRecordAnomaly || got.Reason != recoveryPlanReasonSpecDrift {
		t.Fatalf("drift: %#v", got)
	}
}

// TestPlanRecoveryIsPure 验证计划函数不改变 Store 或 Actual 及其嵌套值。
func TestPlanRecoveryIsPure(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredRunning, domain.StateRunning)
	actual := recoveryPlanActual()
	actual.Main.CapDrop = []string{"ALL"}
	storedBefore := stored
	actualBefore := cloneRecoveryPlanSnapshot(actual)
	_ = PlanRecovery(&stored, &actual)
	if !reflect.DeepEqual(stored, storedBefore) || !reflect.DeepEqual(actual, actualBefore) {
		t.Fatalf("inputs mutated: %#v %#v", stored, actual)
	}
}

// TestPlanRecoveryRejectsInvalidStateAndIdentity 验证未知状态及双方 ID 矛盾只产生 anomaly。
func TestPlanRecoveryRejectsInvalidStateAndIdentity(t *testing.T) {
	stored := recoveryPlanSandbox(domain.DesiredState("Unknown"), domain.StatePending)
	actual := recoveryPlanActual()
	if got := PlanRecovery(&stored, &actual); got.Action != RecoveryActionRecordAnomaly || got.Reason != recoveryPlanReasonStateInvalid {
		t.Fatalf("invalid state: %#v", got)
	}
	stored = recoveryPlanSandbox(domain.DesiredRunning, domain.StateRunning)
	actual.SandboxID = "10010203-0405-4607-8809-0a0b0c0d0e0f"
	if got := PlanRecovery(&stored, &actual); got.Action != RecoveryActionRecordAnomaly || got.Reason != recoveryPlanReasonIdentityConflict {
		t.Fatalf("identity: %#v", got)
	}
}

func recoveryPlanSandbox(desired domain.DesiredState, state domain.SandboxState) domain.Sandbox {
	return domain.Sandbox{ID: actualTestID, DesiredState: desired, ObservedState: state, RuntimeID: "main", SpecHash: strings.Repeat("a", 64), Revision: 7}
}

func recoveryPlanActual() ActualResourceSnapshot {
	main := actualMain(actualTestID, "main", "none")
	volume := actualVolume(actualTestID)
	directory := actualDirectory(actualTestID)
	return ActualResourceSnapshot{SandboxID: actualTestID, Main: &main, Workspace: &volume, Directory: &directory}
}

func cloneRecoveryPlanSnapshot(source ActualResourceSnapshot) ActualResourceSnapshot {
	copy := source
	if source.Main != nil {
		main := cloneContainerObservation(*source.Main)
		copy.Main = &main
	}
	if source.Workspace != nil {
		workspace := *source.Workspace
		copy.Workspace = &workspace
	}
	if source.Directory != nil {
		directory := cloneDirectoryObservation(*source.Directory)
		copy.Directory = &directory
	}
	copy.Anomalies = append([]ActualAnomaly(nil), source.Anomalies...)
	return copy
}
