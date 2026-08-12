package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

var (
	// ErrOrphanImportDisabled 表示配置明确关闭可信 orphan 导入。
	ErrOrphanImportDisabled = errors.New("trusted orphan import is disabled")
	// ErrOrphanLeaseUnknown 表示 bundle 无法证明当前租约，必须转 anomaly。
	ErrOrphanLeaseUnknown = errors.New("trusted orphan lease is unknown")
	// ErrTrustedOrphanExpired 表示可信 bundle 已明确过期，交由 P3-070 删除分支处理。
	ErrTrustedOrphanExpired = errors.New("trusted orphan lease is expired")
)

// OrphanImportStore 是可信 orphan 原子导入及唯一冲突重读所需能力。
type OrphanImportStore interface {
	storeport.RecoveredImporter
	Get(context.Context, string) (domain.Sandbox, error)
}

// OrphanImportResult 描述首次导入或重复恢复命中同一记录的结果。
type OrphanImportResult struct {
	// Sandbox 是已持久化的权威记录。
	Sandbox domain.Sandbox
	// Replayed 表示本次唯一冲突重读到了同一可信导入，而非新建记录。
	Replayed bool
}

// OrphanImportExecutor 验证完整 bundle 后原子导入未过期 orphan。
type OrphanImportExecutor struct {
	store   OrphanImportStore
	clock   Clock
	wake    RecoveryWake
	enabled bool
}

// NewOrphanImportExecutor 创建受 recovery.import_trusted_orphans 控制的导入执行器。
func NewOrphanImportExecutor(store OrphanImportStore, clock Clock, wake RecoveryWake, enabled bool) (*OrphanImportExecutor, error) {
	if store == nil || clock == nil || wake == nil {
		return nil, fmt.Errorf("orphan import dependencies: %w", domain.ErrInvalid)
	}
	return &OrphanImportExecutor{store: store, clock: clock, wake: wake, enabled: enabled}, nil
}

// Execute 导入完整可信、租约已知且未过期的 orphan；不完整 bundle 不会触发 Store 写入或 runtime 动作。
func (e *OrphanImportExecutor) Execute(ctx context.Context, plan RecoveryPlan, actual ActualResourceSnapshot, expected DriftExpectation) (OrphanImportResult, error) {
	if e == nil || e.store == nil || e.clock == nil || e.wake == nil || plan.Action != RecoveryActionImport ||
		plan.SandboxID == "" || plan.SandboxID != actual.SandboxID {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: %w", domain.ErrInvalid)
	}
	if !e.enabled {
		return OrphanImportResult{}, ErrOrphanImportDisabled
	}
	spec, err := RebuildResolvedSpec(actual, expected)
	if err != nil {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: validate bundle: %w", err)
	}
	if actual.Main == nil || actual.Main.CreatedAt.IsZero() || actual.Main.CreatedAt.After(e.clock.Now().UTC()) {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: creation time is untrusted: %w", domain.ErrConflict)
	}
	expiresAt, err := trustedOrphanExpiry(actual)
	if err != nil {
		return OrphanImportResult{}, err
	}
	now := e.clock.Now().UTC()
	if !now.Before(expiresAt) {
		return OrphanImportResult{}, ErrTrustedOrphanExpired
	}
	message, _ := domain.SandboxReasonPublicMessage(domain.SandboxReasonOrphanImported)
	record := domain.Sandbox{
		ID: actual.SandboxID, Spec: spec, SpecHash: spec.Hash(), RuntimeID: actual.Main.ContainerID,
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateCreating,
		Reason: domain.SandboxReasonOrphanImported, Message: message,
		CreatedAt: actual.Main.CreatedAt.UTC(), UpdatedAt: now, LastTransitionAt: now,
		ExpiresAt: timePointer(expiresAt), Origin: domain.SandboxOriginRecoveredOrphan,
	}
	created, err := e.store.ImportRecovered(ctx, storeport.RecoveredImportRequest{Sandbox: record})
	if err == nil {
		_ = e.wake(created.ID)
		return OrphanImportResult{Sandbox: created}, nil
	}
	if !errors.Is(err, domain.ErrConflict) {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: commit: %w", err)
	}
	existing, readErr := e.store.Get(ctx, record.ID)
	if readErr != nil {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: reread conflict: %w", readErr)
	}
	if existing.Origin != domain.SandboxOriginRecoveredOrphan || existing.SpecHash != record.SpecHash ||
		existing.RuntimeID != record.RuntimeID || existing.ExpiresAt == nil || !existing.ExpiresAt.Equal(expiresAt) {
		return OrphanImportResult{}, fmt.Errorf("execute orphan import: ID belongs to another record: %w", domain.ErrConflict)
	}
	_ = e.wake(existing.ID)
	return OrphanImportResult{Sandbox: existing, Replayed: true}, nil
}

func trustedOrphanExpiry(actual ActualResourceSnapshot) (time.Time, error) {
	if actual.Directory != nil && actual.Directory.Manifest != nil {
		manifest := actual.Directory.Manifest
		if manifest.SandboxID == actual.SandboxID && actual.Main != nil && manifest.SpecHash == actual.Main.SpecHash && !manifest.ExpiresAt.IsZero() {
			return manifest.ExpiresAt.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("resolve orphan lease: %w", domain.ErrConflict)
	}
	if actual.Main != nil && actual.Main.SchemaVersion == 2 && actual.Main.CreationExpiresAt != nil && !actual.Main.CreationExpiresAt.IsZero() {
		return actual.Main.CreationExpiresAt.UTC(), nil
	}
	return time.Time{}, ErrOrphanLeaseUnknown
}
