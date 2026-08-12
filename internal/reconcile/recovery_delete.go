package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

const recoveryDeleteCASAttempts = 3

// RecoveryDeleteStore 是残留资源恢复进入普通删除状态机所需的最小 Store 能力。
type RecoveryDeleteStore interface {
	// Get 重读当前权威删除意图和 revision。
	Get(context.Context, string) (domain.Sandbox, error)
	// UpdateObserved 仅提前 reconcile 时间并保留删除所需 runtime identity。
	UpdateObserved(context.Context, storeport.ObservedUpdate) (domain.Sandbox, error)
}

// RecoveryDeleteExecutor 把 DesiredTerminated 的残留资源交回普通 delete reconcile。
type RecoveryDeleteExecutor struct {
	store RecoveryDeleteStore
	clock Clock
	wake  RecoveryWake
}

// NewRecoveryDeleteExecutor 创建不直接访问或删除 runtime 资源的恢复执行器。
func NewRecoveryDeleteExecutor(store RecoveryDeleteStore, clock Clock, wake RecoveryWake) (*RecoveryDeleteExecutor, error) {
	if store == nil || clock == nil || wake == nil {
		return nil, fmt.Errorf("recovery delete dependencies: %w", domain.ErrInvalid)
	}
	return &RecoveryDeleteExecutor{store: store, clock: clock, wake: wake}, nil
}

// Execute 复核删除意图，把 future backoff 提前到当前时刻并 Wake 普通 reconciler。
// 实际资源即使仍在 Running 也不会改变 desired/observed 状态，更不会由恢复路径直接删除。
func (e *RecoveryDeleteExecutor) Execute(ctx context.Context, plan RecoveryPlan, actual ActualResourceSnapshot) (domain.Sandbox, error) {
	if e == nil || e.store == nil || e.clock == nil || e.wake == nil || plan.Action != RecoveryActionWake ||
		plan.SandboxID == "" || actual.SandboxID != plan.SandboxID || !actualHasResources(&actual) {
		return domain.Sandbox{}, fmt.Errorf("execute recovery delete: %w", domain.ErrInvalid)
	}
	for attempt := 0; attempt < recoveryDeleteCASAttempts; attempt++ {
		current, err := e.store.Get(ctx, plan.SandboxID)
		if err != nil {
			return domain.Sandbox{}, fmt.Errorf("execute recovery delete: read store: %w", err)
		}
		if current.ID != actual.SandboxID || current.DesiredState != domain.DesiredTerminated {
			return domain.Sandbox{}, fmt.Errorf("execute recovery delete: intent changed: %w", domain.ErrConflict)
		}
		if len(actual.Anomalies) != 0 {
			return domain.Sandbox{}, fmt.Errorf("execute recovery delete: actual resources are ambiguous: %w", domain.ErrConflict)
		}
		now := e.clock.Now().UTC()
		if current.NextReconcileAt == nil || !current.NextReconcileAt.After(now) {
			_ = e.wake(current.ID)
			return current, nil
		}
		runtimeID := current.RuntimeID
		if actual.Main != nil {
			if actual.Main.SandboxID != current.ID || actual.Main.SpecHash != current.SpecHash {
				return domain.Sandbox{}, fmt.Errorf("execute recovery delete: main identity drift: %w", domain.ErrConflict)
			}
			runtimeID = actual.Main.ContainerID
		}
		updated, err := e.store.UpdateObserved(ctx, storeport.ObservedUpdate{
			ID: current.ID, ExpectedRevision: current.Revision,
			State: current.ObservedState, Reason: current.Reason, Message: current.Message,
			RuntimeID: runtimeID, ReconcileAt: timePointer(now),
		})
		if err == nil {
			_ = e.wake(updated.ID)
			return updated, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return domain.Sandbox{}, fmt.Errorf("execute recovery delete: advance reconcile: %w", err)
		}
	}
	return domain.Sandbox{}, fmt.Errorf("execute recovery delete: CAS retry exhausted: %w", domain.ErrConflict)
}

func timePointer(value time.Time) *time.Time { return &value }

var _ RecoveryDeleteStore = (storeport.Store)(nil)
