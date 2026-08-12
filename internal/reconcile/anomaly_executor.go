package reconcile

import (
	"context"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// RecoveryAnomalyExecutor 是歧义资源的只写诊断执行器。
//
// 该类型刻意不持有 Runtime、导入器、metadata repair 或 wake 端口，因此即使输入来自
// 恶意 label、伪造名称或不完整 bundle，也无法停止、删除、创建或改写任何 runtime 资源。
type RecoveryAnomalyExecutor struct {
	repository storeport.RuntimeAnomalyRepository
}

// NewRecoveryAnomalyExecutor 创建只具备异常持久化能力的恢复执行器。
func NewRecoveryAnomalyExecutor(repository storeport.RuntimeAnomalyRepository) (*RecoveryAnomalyExecutor, error) {
	if repository == nil {
		return nil, fmt.Errorf("anomaly repository: %w", domain.ErrInvalid)
	}
	return &RecoveryAnomalyExecutor{repository: repository}, nil
}

// Execute 按 RECORD_ANOMALY 计划记录单个歧义 bundle；没有显式矛盾的 partial bundle 会生成 incomplete 分类。
func (e *RecoveryAnomalyExecutor) Execute(ctx context.Context, plan RecoveryPlan, actual ActualResourceSnapshot, observedAt time.Time) error {
	if e == nil || e.repository == nil || plan.Action != RecoveryActionRecordAnomaly ||
		plan.SandboxID == "" || plan.SandboxID != actual.SandboxID || observedAt.IsZero() {
		return fmt.Errorf("execute anomaly recovery: %w", domain.ErrInvalid)
	}
	snapshot := cloneRecoveryAnomalySnapshot(actual)
	if len(snapshot.Anomalies) == 0 {
		snapshot.Anomalies = []ActualAnomaly{{Code: ActualAnomalyResourceDamaged, Detail: "INCOMPLETE_BUNDLE"}}
	}
	return RecordActualAnomalies(ctx, e.repository, ActualResourceInventory{Sandboxes: []ActualResourceSnapshot{snapshot}}, observedAt)
}

// RecordUnscoped 记录无法安全归组的资源事实；该路径同样只有 anomaly repository 权限。
func (e *RecoveryAnomalyExecutor) RecordUnscoped(ctx context.Context, anomalies []ActualAnomaly, observedAt time.Time) error {
	if e == nil || e.repository == nil || observedAt.IsZero() {
		return fmt.Errorf("record unscoped anomaly: %w", domain.ErrInvalid)
	}
	return RecordActualAnomalies(ctx, e.repository, ActualResourceInventory{UnscopedAnomalies: append([]ActualAnomaly(nil), anomalies...)}, observedAt)
}

func cloneRecoveryAnomalySnapshot(source ActualResourceSnapshot) ActualResourceSnapshot {
	copy := source
	copy.Anomalies = append([]ActualAnomaly(nil), source.Anomalies...)
	return copy
}
