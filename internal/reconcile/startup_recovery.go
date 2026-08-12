package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

// StartupRecoveryStages 是 inventory 后必须同步完成的恢复、TTL 与最终入队步骤。
type StartupRecoveryStages struct {
	// Recover 执行 Store/actual 关联、异常记录、可信导入和 metadata 修复。
	Recover func(context.Context, ActualResourceInventory, time.Time) error
	// RecoverTTL 在周期 timer 启动前从 Store 重建全部 lease。
	RecoverTTL func(context.Context) error
	// QueueDue 在周期 scanner 启动前确保全部当前 due 记录已经持久化或入队。
	QueueDue func(context.Context) error
}

// StartupRecoveryCoordinator 以单一总超时串行执行启动安全门禁。
type StartupRecoveryCoordinator struct {
	network   runtimeport.EgressRecoveryBootstrap
	inventory runtimeport.RecoveryInventory
	stages    StartupRecoveryStages
	timeout   time.Duration
}

// NewStartupRecoveryCoordinator 创建完整启动恢复协调器；所有 stage 都是必需且必须幂等的。
func NewStartupRecoveryCoordinator(network runtimeport.EgressRecoveryBootstrap, inventory runtimeport.RecoveryInventory,
	stages StartupRecoveryStages, timeout time.Duration) (*StartupRecoveryCoordinator, error) {
	if network == nil || inventory == nil || stages.Recover == nil || stages.RecoverTTL == nil || stages.QueueDue == nil || timeout <= 0 {
		return nil, fmt.Errorf("startup recovery dependencies: %w", domain.ErrInvalid)
	}
	return &StartupRecoveryCoordinator{network: network, inventory: inventory, stages: stages, timeout: timeout}, nil
}

// Run 按 network→inventory→recovery→TTL→queue 顺序执行；任一全局错误或取消都会阻止后续阶段。
func (c *StartupRecoveryCoordinator) Run(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("startup recovery coordinator: %w", domain.ErrInvalid)
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	if err := c.network.EnsureRecoveryEgressNetwork(recoveryCtx); err != nil {
		return fmt.Errorf("ensure recovery egress network: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	scanStartedAt := time.Now().UTC()
	containers, err := c.inventory.InventoryManagedContainers(recoveryCtx)
	if err != nil {
		return fmt.Errorf("inventory managed containers: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	volumes, err := c.inventory.InventoryManagedVolumes(recoveryCtx)
	if err != nil {
		return fmt.Errorf("inventory managed volumes: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	directories, err := c.inventory.InventoryRuntimeDirectories(recoveryCtx)
	if err != nil {
		return fmt.Errorf("inventory runtime directories: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	inventory := AggregateActualResources(containers, volumes, directories)
	if err := c.stages.Recover(recoveryCtx, inventory, scanStartedAt); err != nil {
		return fmt.Errorf("recover inventory: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	if err := c.stages.RecoverTTL(recoveryCtx); err != nil {
		return fmt.Errorf("recover TTL: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	if err := c.stages.QueueDue(recoveryCtx); err != nil {
		return fmt.Errorf("queue due sandboxes: %w", err)
	}
	if err := recoveryCtx.Err(); err != nil {
		return err
	}
	return nil
}

// InventoryRecoveryExecutor 执行纯 planner 产生的有限恢复动作，并隔离单项失败以继续处理其他资源。
type InventoryRecoveryExecutor struct {
	store       storeport.Store
	anomalies   *RecoveryAnomalyExecutor
	metadata    *MetadataRepairExecutor
	orphans     *OrphanImportExecutor
	wake        RecoveryWake
	expectation DriftExpectation
}

// NewInventoryRecoveryExecutor 装配 Store/anomaly/import/wake 边界，trusted orphan 策略由显式开关控制。
func NewInventoryRecoveryExecutor(s storeport.Store, anomalies storeport.RuntimeAnomalyRepository, clock Clock,
	wake RecoveryWake, importTrusted bool, expectation DriftExpectation) (*InventoryRecoveryExecutor, error) {
	if s == nil || anomalies == nil || clock == nil || wake == nil {
		return nil, fmt.Errorf("inventory recovery dependencies: %w", domain.ErrInvalid)
	}
	anomalyExecutor, err := NewRecoveryAnomalyExecutor(anomalies)
	if err != nil {
		return nil, err
	}
	metadata, err := NewMetadataRepairExecutor(s, clock, wake)
	if err != nil {
		return nil, err
	}
	importer, ok := s.(OrphanImportStore)
	if !ok {
		return nil, errors.New("store does not support recovered orphan import")
	}
	orphans, err := NewOrphanImportExecutor(importer, clock, wake, importTrusted)
	if err != nil {
		return nil, err
	}
	return &InventoryRecoveryExecutor{store: s, anomalies: anomalyExecutor, metadata: metadata,
		orphans: orphans, wake: wake, expectation: expectation}, nil
}

// Recover 完整关联 Store 与 actual，对每个资源独立执行动作并在最后解决本轮消失的异常。
func (e *InventoryRecoveryExecutor) Recover(ctx context.Context, inventory ActualResourceInventory, scanStartedAt time.Time) error {
	records, err := e.store.ListAll(ctx)
	if err != nil {
		return err
	}
	stored := make(map[string]domain.Sandbox, len(records))
	actual := make(map[string]ActualResourceSnapshot, len(inventory.Sandboxes))
	ids := make(map[string]struct{}, len(records)+len(inventory.Sandboxes))
	for _, record := range records {
		stored[record.ID], ids[record.ID] = record, struct{}{}
	}
	for _, snapshot := range inventory.Sandboxes {
		actual[snapshot.SandboxID], ids[snapshot.SandboxID] = snapshot, struct{}{}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	var failures error
	if len(inventory.UnscopedAnomalies) != 0 {
		failures = errors.Join(failures, e.anomalies.RecordUnscoped(ctx, inventory.UnscopedAnomalies, scanStartedAt))
	}
	resolvedAt := time.Now().UTC()
	_, resolveErr := FinalizeAnomalyScan(ctx, e.anomalies.repository, inventory,
		InventoryCoverage{ContainersComplete: true, VolumesComplete: true, FilesystemComplete: true}, scanStartedAt, resolvedAt)
	failures = errors.Join(failures, resolveErr)
	for _, id := range ordered {
		var storedPointer *domain.Sandbox
		if value, ok := stored[id]; ok {
			copy := value
			storedPointer = &copy
		}
		var actualPointer *ActualResourceSnapshot
		if value, ok := actual[id]; ok {
			copy := value
			actualPointer = &copy
		}
		plan := PlanRecovery(storedPointer, actualPointer)
		switch plan.Action {
		case RecoveryActionNoOp:
		case RecoveryActionWake:
			_ = e.wake(plan.SandboxID)
		case RecoveryActionRecordAnomaly:
			if actualPointer != nil {
				failures = errors.Join(failures, e.anomalies.Execute(ctx, plan, *actualPointer, scanStartedAt))
			}
		case RecoveryActionRepairMetadata:
			_, actionErr := e.metadata.Execute(ctx, plan, *actualPointer)
			failures = errors.Join(failures, actionErr)
		case RecoveryActionImport:
			_, actionErr := e.orphans.Execute(ctx, plan, *actualPointer, e.expectation)
			failures = errors.Join(failures, actionErr)
		default:
			failures = errors.Join(failures, fmt.Errorf("unknown recovery action %q", plan.Action))
		}
	}
	return failures
}
