package reconcile

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

const metadataRepairCASAttempts = 3

// MetadataRepairStore 是 runtime metadata 修复所需的最小 Store 能力。
type MetadataRepairStore interface {
	// Get 重读当前权威记录；不存在时必须返回 domain.ErrNotFound。
	Get(context.Context, string) (domain.Sandbox, error)
	// UpdateObserved 仅以 CAS 更新观测 metadata，不得改写 spec、expiry 或 desired state。
	UpdateObserved(context.Context, storeport.ObservedUpdate) (domain.Sandbox, error)
}

// RecoveryWake 是 metadata 修复完成后的尽力 reconcile 唤醒函数。
type RecoveryWake func(string) bool

// MetadataRepairExecutor 只执行可信既有 Store 记录的 runtime identity CAS 修复。
type MetadataRepairExecutor struct {
	store MetadataRepairStore
	clock Clock
	wake  RecoveryWake
}

// NewMetadataRepairExecutor 创建不访问 runtime、不导入 orphan 且不启动容器的修复执行器。
func NewMetadataRepairExecutor(store MetadataRepairStore, clock Clock, wake RecoveryWake) (*MetadataRepairExecutor, error) {
	if store == nil || clock == nil || wake == nil {
		return nil, fmt.Errorf("metadata repair dependencies: %w", domain.ErrInvalid)
	}
	return &MetadataRepairExecutor{store: store, clock: clock, wake: wake}, nil
}

// Execute 重读 Store、复核可信 bundle 并以有限 CAS 修复主容器 RuntimeID，随后 Wake。
// DesiredTerminated 记录只修复聚合删除所需的 identity；本方法绝不改变状态、规格或租约。
func (e *MetadataRepairExecutor) Execute(ctx context.Context, plan RecoveryPlan, actual ActualResourceSnapshot) (domain.Sandbox, error) {
	if e == nil || e.store == nil || e.clock == nil || e.wake == nil || plan.Action != RecoveryActionRepairMetadata {
		return domain.Sandbox{}, fmt.Errorf("execute metadata repair: %w", domain.ErrInvalid)
	}
	for attempt := 0; attempt < metadataRepairCASAttempts; attempt++ {
		current, err := e.store.Get(ctx, plan.SandboxID)
		if err != nil {
			return domain.Sandbox{}, fmt.Errorf("execute metadata repair: read store: %w", err)
		}
		if err := validateMetadataRepair(plan, current, actual); err != nil {
			return domain.Sandbox{}, err
		}
		if current.RuntimeID == actual.Main.ContainerID {
			_ = e.wake(current.ID)
			return current, nil
		}
		reconcileAt := e.clock.Now().UTC()
		updated, err := e.store.UpdateObserved(ctx, storeport.ObservedUpdate{
			ID: current.ID, ExpectedRevision: current.Revision,
			State: current.ObservedState, Reason: current.Reason, Message: current.Message,
			RuntimeID: actual.Main.ContainerID, ReconcileAt: &reconcileAt,
		})
		if err == nil {
			_ = e.wake(updated.ID)
			return updated, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return domain.Sandbox{}, fmt.Errorf("execute metadata repair: commit metadata: %w", err)
		}
		// revision 冲突可能来自无关 health/retry 写入；下一轮必须完整重读并重新验证全部语义字段。
	}
	return domain.Sandbox{}, fmt.Errorf("execute metadata repair: CAS retry exhausted: %w", domain.ErrConflict)
}

func validateMetadataRepair(plan RecoveryPlan, stored domain.Sandbox, actual ActualResourceSnapshot) error {
	if plan.SandboxID == "" || plan.SandboxID != stored.ID || plan.SandboxID != actual.SandboxID ||
		plan.ExpectedSpecHash == "" || plan.ExpectedSpecHash != stored.SpecHash ||
		plan.ExpectedRuntimeID == "" || actual.Main == nil || plan.ExpectedRuntimeID != actual.Main.ContainerID {
		return fmt.Errorf("execute metadata repair: stale identity: %w", domain.ErrConflict)
	}
	if actual.Main.SandboxID != stored.ID || actual.Main.Role != runtimeport.ContainerRoleMain ||
		actual.Main.SpecHash != stored.SpecHash || actual.Main.ContainerID == "" {
		return fmt.Errorf("execute metadata repair: untrusted main bundle: %w", domain.ErrConflict)
	}
	if actual.Egress != nil && (actual.Egress.SandboxID != stored.ID || actual.Egress.Role != runtimeport.ContainerRoleEgress) {
		return fmt.Errorf("execute metadata repair: untrusted egress bundle: %w", domain.ErrConflict)
	}
	if actual.Workspace != nil && (actual.Workspace.SandboxID != stored.ID || actual.Workspace.SpecHash != stored.SpecHash) {
		return fmt.Errorf("execute metadata repair: untrusted workspace bundle: %w", domain.ErrConflict)
	}
	if actual.Directory != nil && actual.Directory.Manifest != nil &&
		(actual.Directory.Manifest.SandboxID != stored.ID || actual.Directory.Manifest.SpecHash != stored.SpecHash) {
		return fmt.Errorf("execute metadata repair: untrusted manifest bundle: %w", domain.ErrConflict)
	}
	// 对副本重跑聚合层协议、policy、schema 与 netns 校验，避免相信可能被手工构造或已陈旧的 plan。
	verified := cloneActualSnapshot(actual)
	validateActualBundle(&verified)
	if len(verified.Anomalies) != 0 {
		return fmt.Errorf("execute metadata repair: bundle validation failed: %w", domain.ErrConflict)
	}
	return nil
}

func cloneActualSnapshot(source ActualResourceSnapshot) ActualResourceSnapshot {
	copy := source
	if source.Main != nil {
		main := cloneContainerObservation(*source.Main)
		copy.Main = &main
	}
	if source.Egress != nil {
		egress := cloneContainerObservation(*source.Egress)
		copy.Egress = &egress
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

var _ MetadataRepairStore = (storeport.Store)(nil)
