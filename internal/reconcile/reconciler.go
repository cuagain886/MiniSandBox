// Package reconcile 将持久化的 sandbox 期望状态收敛为 runtime 实际状态。
//
// 本模块负责幂等重试、按 sandbox 串行化、周期调度和 TTL 判断；具体 Docker
// 操作和业务授权分别由 runtime adapter 与 application 层承担。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	store           store.Store
	runtime         runtimeport.Runtime
	probe           RunnerProbe
	shutdown        RunnerShutdown
	locks           *KeyedLock
	clock           Clock
	random          Random
	retryMin        time.Duration
	retryMax        time.Duration
	createLimiter   runtimeport.Limiter
	deleteLimiter   runtimeport.Limiter
	availability    runtimeport.OperationAvailability
	leaseProjector  LeaseProjector
	operationLogger *OperationLogger
}

// LeaseProjector 把 Store 当前租约幂等投影到受管 runtime directory。
type LeaseProjector interface {
	// Write 原子替换固定 lease.json；不得改变 Docker label 或 Store。
	Write(runtimeport.LeaseManifest) error
}

// OperationLimits 是 reconciler 共享 runtime 操作的固定并发上限。
type OperationLimits struct {
	// MaxConcurrentCreates 限制同时进入 Runtime.Ensure 的 sandbox 数量。
	MaxConcurrentCreates int
	// MaxConcurrentDeletes 限制同时进入 Runtime.Delete 的 sandbox 数量。
	MaxConcurrentDeletes int
}

// NewWithShutdown 创建带 runner cancel-all 前置步骤的状态收敛器。
func NewWithShutdown(s store.Store, r runtimeport.Runtime, probe RunnerProbe, shutdown RunnerShutdown) *Reconciler {
	reconciler := New(s, r, probe)
	reconciler.shutdown = shutdown
	return reconciler
}

// New 使用持久化、runtime 和 runner probe 端口创建状态收敛器。
func New(s store.Store, r runtimeport.Runtime, probe RunnerProbe) *Reconciler {
	reconciler := &Reconciler{
		store:    s,
		runtime:  r,
		probe:    probe,
		locks:    NewKeyedLock(),
		clock:    SystemClock{},
		random:   CryptoRandom{},
		retryMin: time.Second,
		retryMax: time.Minute,
	}
	reconciler.availability, _ = r.(runtimeport.OperationAvailability)
	return reconciler
}

// SetLeaseProjector 为 reconciler 装配可选租约投影器；必须在 worker 启动前调用。
func (r *Reconciler) SetLeaseProjector(projector LeaseProjector) {
	r.leaseProjector = projector
}

// SetOperationLogger 为 worker 装配只读安全日志；必须在 worker 启动前调用。
func (r *Reconciler) SetOperationLogger(logger *OperationLogger) {
	r.operationLogger = logger
}

// NewWithRetry 创建使用显式时间、随机源和退避边界的状态收敛器。
func NewWithRetry(s store.Store, r runtimeport.Runtime, probe RunnerProbe, clock Clock, random Random, retryMin, retryMax time.Duration) (*Reconciler, error) {
	if clock == nil || random == nil || retryMin <= 0 || retryMax < retryMin {
		return nil, errors.New("invalid reconciler retry configuration")
	}
	reconciler := New(s, r, probe)
	reconciler.clock, reconciler.random = clock, random
	reconciler.retryMin, reconciler.retryMax = retryMin, retryMax
	return reconciler, nil
}

// NewWithShutdownAndRetry 创建同时支持 runner shutdown 与持久化 retry 的收敛器。
func NewWithShutdownAndRetry(s store.Store, r runtimeport.Runtime, probe RunnerProbe, shutdown RunnerShutdown, clock Clock, random Random, retryMin, retryMax time.Duration) (*Reconciler, error) {
	reconciler, err := NewWithRetry(s, r, probe, clock, random, retryMin, retryMax)
	if err != nil {
		return nil, err
	}
	reconciler.shutdown = shutdown
	return reconciler, nil
}

// NewWithShutdownRetryAndLimits 创建生产用收敛器并装配独立 create/delete 门禁。
func NewWithShutdownRetryAndLimits(s store.Store, r runtimeport.Runtime, probe RunnerProbe, shutdown RunnerShutdown, clock Clock, random Random, retryMin, retryMax time.Duration, limits OperationLimits) (*Reconciler, error) {
	reconciler, err := NewWithShutdownAndRetry(s, r, probe, shutdown, clock, random, retryMin, retryMax)
	if err != nil {
		return nil, err
	}
	reconciler.createLimiter, err = runtimeport.NewLimiter(limits.MaxConcurrentCreates)
	if err != nil {
		return nil, fmt.Errorf("configure create limiter: %w", err)
	}
	reconciler.deleteLimiter, err = runtimeport.NewLimiter(limits.MaxConcurrentDeletes)
	if err != nil {
		return nil, fmt.Errorf("configure delete limiter: %w", err)
	}
	return reconciler, nil
}

// Reconcile 对指定 sandbox 执行一次幂等收敛。
//
// 同一 ID 先通过 keyed lock 串行化，再从 Store 重读最新 revision；内存 wake
// 携带的旧 snapshot 从不参与状态决策。
func (r *Reconciler) Reconcile(ctx context.Context, sandboxID string) (resultErr error) {
	if r.operationLogger != nil {
		started := r.operationLogger.reconcileStart(ctx, sandboxID)
		defer func() { r.operationLogger.reconcileResult(ctx, sandboxID, started, resultErr) }()
	}
	unlock, err := r.locks.LockContext(ctx, sandboxID)
	if err != nil {
		return err
	}
	defer unlock()

	sandbox, err := r.store.Get(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("read sandbox for reconcile: %w", err)
	}
	sandbox, err = r.expireLeaseIfDue(ctx, sandbox)
	if err != nil {
		return err
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

// expireLeaseIfDue 让周期 scanner 在 keyed lock 内复用 Store 的 TTL CAS。
// 成功后直接返回 DesiredTerminated snapshot，继续进入既有删除收敛路径。
func (r *Reconciler) expireLeaseIfDue(ctx context.Context, sandbox domain.Sandbox) (domain.Sandbox, error) {
	now := r.clock.Now().UTC()
	if !sandboxLeaseDue(sandbox, now) {
		return sandbox, nil
	}
	updated, err := r.store.ExpireIntent(ctx, store.ExpireIntentUpdate{
		ID: sandbox.ID, ExpectedRevision: sandbox.Revision,
		ExpectedExpiresAt: sandbox.ExpiresAt.UTC(), Now: now,
	})
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("submit scanned TTL expiry: %w", err)
	}
	return updated, nil
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
		if replacer, ok := r.runtime.(runtimeport.ComputeReplacer); ok {
			return r.recoverRunning(ctx, sandbox, replacer)
		}
		if err := r.projectLease(sandbox); err != nil {
			return r.recordFailure(ctx, sandbox, err, sandbox.RuntimeID, RetryOperationRecover)
		}
		return r.resetSettledRetry(ctx, sandbox, domain.StateRunning, reasonRunning, messageRunning, sandbox.RuntimeID)
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

	actual, err := r.ensureRuntime(ctx, creating)
	if err != nil {
		var waitErr *operationLimitWaitError
		if errors.As(err, &waitErr) {
			return err
		}
		return r.failRunning(ctx, creating, err, RetryOperationCreate)
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
			return r.failRunning(ctx, waiting, errors.New("outbound readiness gate is unavailable"), RetryOperationStart)
		}
		identity, err := networkProbe.ProbeNetwork(ctx, waiting.ID, actual.RunnerProtocolVersion)
		if err != nil {
			return r.failRunning(ctx, waiting, err, RetryOperationStart)
		}
		if err := egressGate.CheckSandboxEgress(ctx, waiting.ID, identity); err != nil {
			return r.failRunning(ctx, waiting, err, RetryOperationStart)
		}
	} else if err := r.probe.Probe(ctx, waiting.ID, actual.RunnerProtocolVersion); err != nil {
		return r.failRunning(ctx, waiting, err, RetryOperationStart)
	}
	running, err := r.store.ResetRetry(ctx, store.RetryResetUpdate{
		Observed: store.ObservedUpdate{
			ID: waiting.ID, ExpectedRevision: waiting.Revision, State: domain.StateRunning,
			Reason: reasonRunning, Message: messageRunning, RuntimeID: actual.RuntimeID,
		},
		ReconciledAt: r.clock.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("mark sandbox running: %w", err)
	}
	if err := r.projectLease(running); err != nil {
		return r.recordFailure(ctx, running, err, running.RuntimeID, RetryOperationRecover)
	}
	return nil
}

func (r *Reconciler) projectLease(sandbox domain.Sandbox) error {
	if r.leaseProjector == nil || sandbox.DesiredState != domain.DesiredRunning ||
		sandbox.ObservedState != domain.StateRunning {
		return nil
	}
	if sandbox.ExpiresAt == nil || sandbox.Revision == 0 {
		return fmt.Errorf("project sandbox lease: %w", store.ErrCorrupt)
	}
	return r.leaseProjector.Write(runtimeport.LeaseManifest{
		SchemaVersion: runtimeport.LeaseManifestSchemaVersion,
		SandboxID:     sandbox.ID, SpecHash: sandbox.SpecHash, ExpiresAt: sandbox.ExpiresAt.UTC(),
		ProjectedStoreRevision: sandbox.Revision,
	})
}

// ensureRuntime 在 keyed lock 内等待全局 create 配额；未取得配额时直接返回
// context 错误，不进入失败记账。defer 保证 Runtime panic 也会释放配额。
func (r *Reconciler) ensureRuntime(ctx context.Context, sandbox domain.Sandbox) (runtimeport.ActualSandbox, error) {
	if r.availability != nil {
		if err := r.availability.WaitAvailable(ctx); err != nil {
			return runtimeport.ActualSandbox{}, &operationLimitWaitError{cause: err}
		}
	}
	if r.createLimiter == nil {
		return r.runtime.Ensure(ctx, sandbox)
	}
	release, err := r.createLimiter.Acquire(ctx)
	if err != nil {
		return runtimeport.ActualSandbox{}, &operationLimitWaitError{cause: err}
	}
	defer release()
	return r.runtime.Ensure(ctx, sandbox)
}

// operationLimitWaitError 区分尚未开始 runtime 操作的等待取消，防止增加 retry attempt。
type operationLimitWaitError struct{ cause error }

func (e *operationLimitWaitError) Error() string { return "runtime operation limit wait canceled" }
func (e *operationLimitWaitError) Unwrap() error { return e.cause }

// reconcileTerminated 把任意非 Terminated 记录先推进到 Stopping 再清理。
func (r *Reconciler) reconcileTerminated(
	ctx context.Context,
	sandbox domain.Sandbox,
) error {
	if sandbox.ObservedState == domain.StateTerminated {
		return r.resetSettledRetry(ctx, sandbox, domain.StateTerminated, reasonTerminated, messageTerminated, "")
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
	if err := r.deleteRuntime(ctx, stopping.ID); err != nil {
		var waitErr *operationLimitWaitError
		if errors.As(err, &waitErr) {
			return err
		}
		if ClassifyRetryError(RetryOperationDelete, err).Successful {
			// runtime 已不存在等价于删除目标满足，继续提交 Terminated。
		} else {
			return r.recordFailure(
				ctx,
				stopping,
				&cleanupPendingFailure{cause: err},
				stopping.RuntimeID,
				RetryOperationDelete,
			)
		}
	}
	_, err = r.store.ResetRetry(ctx, store.RetryResetUpdate{
		Observed: store.ObservedUpdate{
			ID: stopping.ID, ExpectedRevision: stopping.Revision, State: domain.StateTerminated,
			Reason: reasonTerminated, Message: messageTerminated, RuntimeID: "",
		},
		ReconciledAt: r.clock.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("mark sandbox terminated: %w", err)
	}
	return nil
}

// resetSettledRetry 清理旧版本或异常恢复遗留在最终状态上的 retry 调度信息。
// 已经清零时保持真正 no-op，避免周期健康扫描无意义地递增 revision。
func (r *Reconciler) resetSettledRetry(ctx context.Context, sandbox domain.Sandbox, state domain.SandboxState, reason, message, runtimeID string) error {
	if sandbox.RetryAttempt == 0 && sandbox.NextReconcileAt == nil {
		return nil
	}
	_, err := r.store.ResetRetry(ctx, store.RetryResetUpdate{
		Observed: store.ObservedUpdate{
			ID: sandbox.ID, ExpectedRevision: sandbox.Revision, State: state,
			Reason: reason, Message: message, RuntimeID: runtimeID,
		},
		ReconciledAt: r.clock.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("reset settled sandbox retry: %w", err)
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
	operation RetryOperation,
) error {
	cleanupErr := r.deleteRuntime(ctx, sandbox.ID)
	var waitErr *operationLimitWaitError
	if errors.As(cleanupErr, &waitErr) {
		return errors.Join(operationErr, cleanupErr)
	}
	if cleanupErr != nil && !ClassifyRetryError(RetryOperationCleanup, cleanupErr).Successful {
		return r.recordFailure(
			ctx,
			sandbox,
			&cleanupPendingFailure{
				cause: errors.Join(operationErr, cleanupErr),
			},
			sandbox.RuntimeID,
			RetryOperationCleanup,
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
	return r.recordFailure(ctx, sandbox, failureErr, "", operation)
}

// deleteRuntime 在所有删除、失败补偿及后续恢复清理路径之间共享全局 delete 配额。
// slot 只覆盖实际 Runtime.Delete 调用，等待取消不会改写 Store intent。
func (r *Reconciler) deleteRuntime(ctx context.Context, sandboxID string) error {
	if r.availability != nil {
		if err := r.availability.WaitAvailable(ctx); err != nil {
			return &operationLimitWaitError{cause: err}
		}
	}
	if r.deleteLimiter == nil {
		return r.runtime.Delete(ctx, sandboxID)
	}
	release, err := r.deleteLimiter.Acquire(ctx)
	if err != nil {
		return &operationLimitWaitError{cause: err}
	}
	defer release()
	return r.runtime.Delete(ctx, sandboxID)
}

// recordFailure 用当前 revision CAS 写入安全的 Failed 状态并保留原始 cause。
func (r *Reconciler) recordFailure(
	ctx context.Context,
	sandbox domain.Sandbox,
	failureErr error,
	runtimeID string,
	operation RetryOperation,
) error {
	failure := runtimeport.ClassifyError(failureErr)
	classification := ClassifyRetryError(operation, failureErr)
	decision, decisionErr := DecideRetry(RetryPolicyInput{
		Operation: operation, ErrorClass: classification.ErrorClass, Attempt: sandbox.RetryAttempt,
	})
	if decisionErr != nil {
		return errors.Join(failureErr, decisionErr)
	}
	if decision.Action == RetryActionRetryAt {
		delay, err := FullJitterBackoff(sandbox.RetryAttempt, r.retryMin, r.retryMax, r.random)
		if err != nil {
			return errors.Join(failureErr, err)
		}
		attemptedAt := r.clock.Now().UTC()
		_, updateErr := r.store.ScheduleRetry(ctx, store.RetryUpdate{
			ID: sandbox.ID, ExpectedRevision: sandbox.Revision,
			AttemptedAt: attemptedAt, NextReconcileAt: attemptedAt.Add(delay),
			Reason: failure.Reason, Message: failure.Message, RuntimeID: runtimeID,
		})
		if updateErr != nil {
			if errors.Is(updateErr, domain.ErrConflict) {
				_, _ = r.store.Get(ctx, sandbox.ID)
			}
			if r.operationLogger != nil {
				result := "persist_failure"
				if errors.Is(updateErr, domain.ErrConflict) {
					result = "cas_conflict"
				}
				r.operationLogger.retryDecision(ctx, sandbox.ID, operation, sandbox.RetryAttempt+1,
					delay, classification.ErrorClass, result)
			}
			return errors.Join(failureErr, fmt.Errorf("schedule sandbox retry: %w", updateErr))
		}
		if r.operationLogger != nil {
			r.operationLogger.retryDecision(ctx, sandbox.ID, operation, sandbox.RetryAttempt+1,
				delay, classification.ErrorClass, "scheduled")
		}
		return failureErr
	}
	if decision.Action == RetryActionImmediateReread {
		_, _ = r.store.Get(ctx, sandbox.ID)
		if r.operationLogger != nil {
			r.operationLogger.retryDecision(ctx, sandbox.ID, operation, sandbox.RetryAttempt, 0,
				classification.ErrorClass, "reread")
		}
		return failureErr
	}
	if classification.ErrorClass == RetryErrorShutdown {
		if r.operationLogger != nil {
			r.operationLogger.retryDecision(ctx, sandbox.ID, operation, sandbox.RetryAttempt, 0,
				classification.ErrorClass, "canceled")
		}
		return failureErr
	}
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
	if r.operationLogger != nil {
		r.operationLogger.retryDecision(ctx, sandbox.ID, operation, sandbox.RetryAttempt+1, 0,
			classification.ErrorClass, "terminal")
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
