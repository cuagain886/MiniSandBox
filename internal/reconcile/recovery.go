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
	// RecoveryIssueOrphanRuntime 表示 Docker 资源没有对应 Store 记录。
	RecoveryIssueOrphanRuntime = "ORPHAN_RUNTIME"
	// RecoveryIssueSpecDrift 表示 Docker spec hash 与 Store 权威记录不一致。
	RecoveryIssueSpecDrift = "SPEC_DRIFT"
	// RecoveryIssueDuplicateRuntime 表示同一 sandbox ID 发现多个容器，无法安全关联。
	RecoveryIssueDuplicateRuntime = "DUPLICATE_RUNTIME"
)

// RecoveryDiagnostic 是不包含 labels、路径和 Docker 原始响应的安全恢复告警。
type RecoveryDiagnostic struct {
	// Code 是稳定机器可读诊断码。
	Code string
	// SandboxID 是 labels 能安全解析时得到的 sandbox ID，损坏严重时可为空。
	SandboxID string
}

// RecoveryReporter 接收恢复期间发现的非致命安全诊断。
type RecoveryReporter func(RecoveryDiagnostic)

// RecoveryReadiness 接收启动恢复是否完整成功的状态。
type RecoveryReadiness interface {
	// SetRecovery 设置 recovery readiness；只有完整对账成功后才能传 true。
	SetRecovery(bool)
}

// RecoveryService 对账 Store 记录与 Docker 受管容器并唤醒 reconciler。
//
// 服务不导入、删除或修复没有 Store 记录的 orphan，也不解释损坏 labels；
// 这些资源只产生安全诊断，完整 orphan 策略留到后续阶段。
type RecoveryService struct {
	store     store.Store
	runtime   runtimeport.Runtime
	queue     *WakeQueue
	readiness RecoveryReadiness
	report    RecoveryReporter
}

// NewRecoveryService 创建启动恢复服务。
func NewRecoveryService(
	s store.Store,
	runtime runtimeport.Runtime,
	queue *WakeQueue,
	readiness RecoveryReadiness,
	report RecoveryReporter,
) (*RecoveryService, error) {
	if s == nil {
		return nil, errors.New("recovery store must not be nil")
	}
	if runtime == nil {
		return nil, errors.New("recovery runtime must not be nil")
	}
	if queue == nil {
		return nil, errors.New("recovery queue must not be nil")
	}
	if readiness == nil {
		return nil, errors.New("recovery readiness must not be nil")
	}
	return &RecoveryService{
		store:     s,
		runtime:   runtime,
		queue:     queue,
		readiness: readiness,
		report:    report,
	}, nil
}

// Run 执行一次完整启动对账。
//
// 开始时先强制 readiness=false；扫描、关联、CAS 更新和入队全部成功后才
// 设置 true。返回错误时已经入队的 ID 可以保留，但服务仍不可宣告 ready。
func (s *RecoveryService) Run(ctx context.Context) error {
	s.readiness.SetRecovery(false)

	actual, err := s.runtime.ListManaged(ctx)
	if err != nil {
		return fmt.Errorf("list managed runtimes for recovery: %w", err)
	}
	records, err := s.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list stored sandboxes for recovery: %w", err)
	}
	// candidates 是 Store 对“仍需收敛”的权威判断；以 ListAll 数量作为
	// 单批上限，避免固定常量在大仓库中静默截断启动恢复。
	limit := len(records)
	if limit == 0 {
		limit = 1
	}
	now := time.Now().UTC()
	candidates, err := s.store.ListReconcileCandidates(
		ctx,
		store.ReconcileCandidateQuery{
			Now:           now,
			RunningCutoff: now,
			Limit:         limit,
		},
	)
	if err != nil {
		return fmt.Errorf("list reconcile candidates for recovery: %w", err)
	}

	storedByID := make(map[string]domain.Sandbox, len(records))
	for _, sandbox := range records {
		storedByID[sandbox.ID] = sandbox
	}
	actualByID := s.indexActual(actual)

	for _, candidate := range candidates {
		s.queue.Wake(candidate.ID)
	}
	for _, sandbox := range records {
		runtime, exists := actualByID[sandbox.ID]
		if !exists {
			// DesiredRunning 和 DesiredTerminated 都必须重新进入状态机；
			// 对稳定 Terminated 的重复唤醒最终会成为幂等 no-op。
			s.queue.Wake(sandbox.ID)
			continue
		}
		if runtime.SpecHash != sandbox.SpecHash {
			s.reportDiagnostic(RecoveryDiagnostic{
				Code:      RecoveryIssueSpecDrift,
				SandboxID: sandbox.ID,
			})
		}
		if runtime.RuntimeID != sandbox.RuntimeID {
			reconcileAt := now
			updated, err := s.store.UpdateObserved(
				ctx,
				store.ObservedUpdate{
					ID:               sandbox.ID,
					ExpectedRevision: sandbox.Revision,
					State:            sandbox.ObservedState,
					Reason:           sandbox.Reason,
					Message:          sandbox.Message,
					RuntimeID:        runtime.RuntimeID,
					ReconcileAt:      &reconcileAt,
				},
			)
			if err != nil {
				return fmt.Errorf(
					"restore sandbox runtime identity: %w",
					err,
				)
			}
			storedByID[sandbox.ID] = updated
		}
		s.queue.Wake(sandbox.ID)
	}
	for sandboxID := range actualByID {
		if _, exists := storedByID[sandboxID]; !exists {
			s.reportDiagnostic(RecoveryDiagnostic{
				Code:      RecoveryIssueOrphanRuntime,
				SandboxID: sandboxID,
			})
		}
	}

	s.readiness.SetRecovery(true)
	return nil
}

// indexActual 过滤损坏项并拒绝把重复 ID 中的任一容器关联到 Store。
func (s *RecoveryService) indexActual(
	actual []runtimeport.ActualSandbox,
) map[string]runtimeport.ActualSandbox {
	indexed := make(map[string]runtimeport.ActualSandbox, len(actual))
	duplicates := make(map[string]struct{})
	for _, runtime := range actual {
		if runtime.DiscoveryIssue != "" {
			s.reportDiagnostic(RecoveryDiagnostic{
				Code:      runtime.DiscoveryIssue,
				SandboxID: runtime.ID,
			})
			continue
		}
		if runtime.ID == "" {
			s.reportDiagnostic(RecoveryDiagnostic{
				Code: runtimeport.DiscoveryLabelsInvalid,
			})
			continue
		}
		if _, exists := indexed[runtime.ID]; exists {
			delete(indexed, runtime.ID)
			duplicates[runtime.ID] = struct{}{}
			s.reportDiagnostic(RecoveryDiagnostic{
				Code:      RecoveryIssueDuplicateRuntime,
				SandboxID: runtime.ID,
			})
			continue
		}
		if _, duplicated := duplicates[runtime.ID]; duplicated {
			continue
		}
		indexed[runtime.ID] = runtime
	}
	return indexed
}

// reportDiagnostic 隔离可选 reporter 的 panic，避免诊断逻辑阻断恢复。
func (s *RecoveryService) reportDiagnostic(diagnostic RecoveryDiagnostic) {
	if s.report == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	s.report(diagnostic)
}
