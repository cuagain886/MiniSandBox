// Package reconcile 将持久化的 sandbox 期望状态收敛为 runtime 实际状态。
//
// 本模块负责幂等重试、按 sandbox 串行化、周期调度和 TTL 判断；具体 Docker
// 操作和业务授权分别由 runtime adapter 与 application 层承担。
package reconcile

import (
	"context"
	"fmt"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

const (
	reasonCreatingRuntime = "CREATING_RUNTIME"
	reasonWaitingRunner   = "WAITING_RUNNER"
	reasonRunning         = "RUNNING"
	reasonDeletingRuntime = "DELETING_RUNTIME"
	reasonTerminated      = "TERMINATED"

	messageCreatingRuntime = "Preparing sandbox runtime."
	messageWaitingRunner   = "Waiting for sandbox runner."
	messageRunning         = "Sandbox is running."
	messageDeletingRuntime = "Deleting sandbox runtime."
	messageTerminated      = "Sandbox runtime has been deleted."
)

// Reconciler 将单个 sandbox 的期望状态幂等收敛到 runtime 实际状态。
type Reconciler struct {
	store   store.Store
	runtime runtimeport.Runtime
	probe   RunnerProbe
	locks   *KeyedLock
}

// New 使用持久化、runtime 和 runner probe 端口创建状态收敛器。
func New(s store.Store, r runtimeport.Runtime, probe RunnerProbe) *Reconciler {
	return &Reconciler{
		store:   s,
		runtime: r,
		probe:   probe,
		locks:   NewKeyedLock(),
	}
}

// Reconcile 对指定 sandbox 执行一次幂等收敛。
//
// 同一 ID 先通过 keyed lock 串行化，再从 Store 重读最新 revision；内存 wake
// 携带的旧 snapshot 从不参与状态决策。
func (r *Reconciler) Reconcile(ctx context.Context, sandboxID string) error {
	unlock := r.locks.Lock(sandboxID)
	defer unlock()

	sandbox, err := r.store.Get(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("read sandbox for reconcile: %w", err)
	}
	switch sandbox.DesiredState {
	case domain.DesiredRunning:
		return r.reconcileRunning(ctx, sandbox)
	case domain.DesiredTerminated:
		return r.reconcileTerminated(ctx, sandbox)
	default:
		return fmt.Errorf("sandbox desired state is invalid: %w", domain.ErrInvalid)
	}
}

// reconcileRunning 把 DesiredRunning 记录从任意未完成创建态推进到 Running。
func (r *Reconciler) reconcileRunning(
	ctx context.Context,
	sandbox domain.Sandbox,
) error {
	if sandbox.ObservedState == domain.StateRunning {
		return nil
	}
	creating, err := r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               sandbox.ID,
		ExpectedRevision: sandbox.Revision,
		State:            domain.StateCreating,
		Reason:           reasonCreatingRuntime,
		Message:          messageCreatingRuntime,
		RuntimeID:        sandbox.RuntimeID,
	})
	if err != nil {
		return fmt.Errorf("mark sandbox runtime creating: %w", err)
	}

	actual, err := r.runtime.Ensure(ctx, creating)
	if err != nil {
		return fmt.Errorf("ensure sandbox runtime: %w", err)
	}
	waiting, err := r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               creating.ID,
		ExpectedRevision: creating.Revision,
		State:            domain.StateCreating,
		Reason:           reasonWaitingRunner,
		Message:          messageWaitingRunner,
		RuntimeID:        actual.RuntimeID,
	})
	if err != nil {
		return fmt.Errorf("mark sandbox waiting for runner: %w", err)
	}
	if err := r.probe.Probe(ctx, waiting.ID); err != nil {
		return fmt.Errorf("probe sandbox runner: %w", err)
	}
	_, err = r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               waiting.ID,
		ExpectedRevision: waiting.Revision,
		State:            domain.StateRunning,
		Reason:           reasonRunning,
		Message:          messageRunning,
		RuntimeID:        actual.RuntimeID,
	})
	if err != nil {
		return fmt.Errorf("mark sandbox running: %w", err)
	}
	return nil
}

// reconcileTerminated 把任意非 Terminated 记录先推进到 Stopping 再清理。
func (r *Reconciler) reconcileTerminated(
	ctx context.Context,
	sandbox domain.Sandbox,
) error {
	if sandbox.ObservedState == domain.StateTerminated {
		return nil
	}
	stopping, err := r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               sandbox.ID,
		ExpectedRevision: sandbox.Revision,
		State:            domain.StateStopping,
		Reason:           reasonDeletingRuntime,
		Message:          messageDeletingRuntime,
		RuntimeID:        sandbox.RuntimeID,
	})
	if err != nil {
		return fmt.Errorf("mark sandbox runtime deleting: %w", err)
	}
	if err := r.runtime.Delete(ctx, stopping.ID); err != nil {
		// 删除未确认完成时必须保留 Stopping；P1-060/P1-062 再负责失败分类
		// 和 Failed 状态，当前任务绝不提前写 Terminated。
		return fmt.Errorf("delete sandbox runtime: %w", err)
	}
	_, err = r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               stopping.ID,
		ExpectedRevision: stopping.Revision,
		State:            domain.StateTerminated,
		Reason:           reasonTerminated,
		Message:          messageTerminated,
		RuntimeID:        "",
	})
	if err != nil {
		return fmt.Errorf("mark sandbox terminated: %w", err)
	}
	return nil
}
