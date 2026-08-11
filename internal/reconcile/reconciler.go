// Package reconcile 将持久化的 sandbox 期望状态收敛为 runtime 实际状态。
//
// 本模块负责幂等重试、按 sandbox 串行化、周期调度和 TTL 判断；具体 Docker
// 操作和业务授权分别由 runtime adapter 与 application 层承担。
package reconcile

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

const (
	reasonCreatingRuntime = domain.SandboxReasonCreatingRuntime
	reasonWaitingRunner   = domain.SandboxReasonWaitingRunner
	reasonRunning         = domain.SandboxReasonRunning
	reasonDeletingRuntime = domain.SandboxReasonDeletingRuntime
	reasonTerminated      = domain.SandboxReasonTerminated

	messageCreatingRuntime = "Preparing sandbox runtime."
	messageWaitingRunner   = "Waiting for sandbox runner."
	messageRunning         = "Sandbox is running."
	messageDeletingRuntime = "Deleting sandbox runtime."
	messageTerminated      = "Sandbox runtime has been deleted."
	reasonEgressUnhealthy  = domain.SandboxReasonEgressUnhealthy
	messageEgressUnhealthy = "Sandbox outbound isolation is unhealthy."
)

// Reconciler 将单个 sandbox 的期望状态幂等收敛到 runtime 实际状态。
type Reconciler struct {
	store    store.Store
	runtime  runtimeport.Runtime
	probe    RunnerProbe
	shutdown RunnerShutdown
	locks    *KeyedLock
}

// NewWithShutdown 创建带 runner cancel-all 前置步骤的状态收敛器。
func NewWithShutdown(s store.Store, r runtimeport.Runtime, probe RunnerProbe, shutdown RunnerShutdown) *Reconciler {
	reconciler := New(s, r, probe)
	reconciler.shutdown = shutdown
	return reconciler
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
	unlock, err := r.locks.LockContext(ctx, sandboxID)
	if err != nil {
		return err
	}
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

// FailEgress 以 CAS 记录已确认的 outbound 隔离漂移，并永久关闭当前 runner 准入。
//
// 本操作保留 DesiredRunning，等待调用方显式删除；重复观察保持稳定失败原因，不触发透明重建。
func (r *Reconciler) FailEgress(ctx context.Context, sandboxID string) error {
	unlock, err := r.locks.LockContext(ctx, sandboxID)
	if err != nil {
		return err
	}
	defer unlock()
	sandbox, err := r.store.Get(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("read sandbox for egress failure: %w", err)
	}
	if sandbox.DesiredState != domain.DesiredRunning || sandbox.ObservedState == domain.StateTerminated || sandbox.ObservedState == domain.StateFailed && sandbox.Reason == reasonEgressUnhealthy {
		return nil
	}
	failed, err := r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID: sandbox.ID, ExpectedRevision: sandbox.Revision, State: domain.StateFailed,
		Reason: reasonEgressUnhealthy, Message: messageEgressUnhealthy, RuntimeID: sandbox.RuntimeID,
	})
	if err != nil {
		return fmt.Errorf("record egress unhealthy: %w", err)
	}
	if r.shutdown != nil {
		_ = r.shutdown.Shutdown(ctx, failed.ID)
	}
	return nil
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
		return r.failRunning(ctx, creating, err)
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
	if waiting.Spec.Network.Outbound {
		networkProbe, ok := r.probe.(RunnerNetworkProbe)
		egressGate, runtimeOK := r.runtime.(runtimeport.ExecutionEgressGate)
		if !ok || !runtimeOK {
			return r.failRunning(ctx, waiting, errors.New("outbound readiness gate is unavailable"))
		}
		identity, err := networkProbe.ProbeNetwork(ctx, waiting.ID, actual.RunnerProtocolVersion)
		if err != nil {
			return r.failRunning(ctx, waiting, err)
		}
		if err := egressGate.CheckSandboxEgress(ctx, waiting.ID, identity); err != nil {
			return r.failRunning(ctx, waiting, err)
		}
	} else if err := r.probe.Probe(ctx, waiting.ID, actual.RunnerProtocolVersion); err != nil {
		return r.failRunning(ctx, waiting, err)
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
	// runner 关闭只用于缩短进程退出窗口；Docker 主容器删除才是不可绕过的永久清理边界。
	if r.shutdown != nil {
		_ = r.shutdown.Shutdown(ctx, stopping.ID)
	}
	if err := r.runtime.Delete(ctx, stopping.ID); err != nil {
		return r.recordFailure(
			ctx,
			stopping,
			&cleanupPendingFailure{cause: err},
			stopping.RuntimeID,
		)
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

// failRunning 清理由创建或 runner probe 失败遗留的全部 runtime 资源。
//
// Runtime.Ensure 已补偿本次调用的副作用，这里仍幂等 Delete 一次，以覆盖
// 进程崩溃前留下、未被本次 operation journal 记录的旧 partial resource。
func (r *Reconciler) failRunning(
	ctx context.Context,
	sandbox domain.Sandbox,
	operationErr error,
) error {
	cleanupErr := r.runtime.Delete(ctx, sandbox.ID)
	if cleanupErr != nil {
		return r.recordFailure(
			ctx,
			sandbox,
			&cleanupPendingFailure{
				cause: errors.Join(operationErr, cleanupErr),
			},
			sandbox.RuntimeID,
		)
	}

	// Ensure 内部补偿曾失败、但这里的全量 Delete 已成功时，恢复真正触发
	// 创建失败的 reason，避免已经清干净的记录继续伪装为待清理。
	failureErr := operationErr
	var compensated interface {
		OperationError() error
	}
	if errors.As(operationErr, &compensated) &&
		compensated.OperationError() != nil {
		failureErr = compensated.OperationError()
	}
	return r.recordFailure(ctx, sandbox, failureErr, "")
}

// recordFailure 用当前 revision CAS 写入安全的 Failed 状态并保留原始 cause。
func (r *Reconciler) recordFailure(
	ctx context.Context,
	sandbox domain.Sandbox,
	failureErr error,
	runtimeID string,
) error {
	failure := runtimeport.ClassifyError(failureErr)
	_, updateErr := r.store.UpdateObserved(ctx, store.ObservedUpdate{
		ID:               sandbox.ID,
		ExpectedRevision: sandbox.Revision,
		State:            domain.StateFailed,
		Reason:           failure.Reason,
		Message:          failure.Message,
		RuntimeID:        runtimeID,
	})
	if updateErr != nil {
		return errors.Join(failureErr, fmt.Errorf(
			"record sandbox failure: %w",
			updateErr,
		))
	}
	return failureErr
}

// cleanupPendingFailure 强制把未完成的资源删除映射为可重试清理状态。
type cleanupPendingFailure struct {
	cause error
}

// Error 返回不会泄露底层删除错误的固定安全文案。
func (*cleanupPendingFailure) Error() string {
	return "sandbox runtime cleanup is pending"
}

// Unwrap 保留内部创建与删除 cause，供日志和 errors.Is 使用。
func (e *cleanupPendingFailure) Unwrap() error {
	return e.cause
}

// FailureReason 返回稳定的 cleanup pending 生命周期 reason。
func (*cleanupPendingFailure) FailureReason() string {
	return runtimeport.FailureReasonCleanupPending
}
