package application

import (
	"context"
	"fmt"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
)

const (
	// createAcceptedReason 是创建意图首次持久化时的稳定机器可读原因。
	createAcceptedReason = "CREATE_ACCEPTED"
	// createAcceptedMessage 是不会泄露内部状态的固定创建受理文案。
	createAcceptedMessage = "Sandbox creation has been accepted."
)

// SandboxService 编排 sandbox 生命周期用例及其持久化访问。
type SandboxService struct {
	store       store.Store
	idGenerator IDGenerator
	clock       Clock
	specBuilder SandboxSpecBuilder
}

// NewSandboxService 使用显式依赖创建可确定测试的生命周期服务。
//
// 本构造函数不接受 Runtime 或 wake queue；Phase 1 创建用例只提交持久化意图，
// 不同步等待容器创建。
func NewSandboxService(
	s store.Store,
	idGenerator IDGenerator,
	clock Clock,
	specBuilder SandboxSpecBuilder,
) *SandboxService {
	return &SandboxService{
		store:       s,
		idGenerator: idGenerator,
		clock:       clock,
		specBuilder: specBuilder,
	}
}

// Create 构建并持久化一条 Pending/DesiredRunning sandbox 创建意图。
//
// spec 校验和 ID 生成发生在持久化前；Store.Create 只调用一次。成功返回不
// 表示 runtime 已创建，reconcile 唤醒将在后续任务中单独装配。
func (s *SandboxService) Create(
	ctx context.Context,
	command CreateSandbox,
) (domain.Sandbox, error) {
	spec, err := s.specBuilder.Build(command)
	if err != nil {
		return domain.Sandbox{}, err
	}

	id, err := s.idGenerator.NewID()
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("generate sandbox ID: %w", err)
	}
	now := s.clock.Now().UTC()
	sandbox := domain.Sandbox{
		ID:               id,
		Spec:             spec,
		DesiredState:     domain.DesiredRunning,
		ObservedState:    domain.StatePending,
		Reason:           createAcceptedReason,
		Message:          createAcceptedMessage,
		RuntimeID:        "",
		SpecHash:         spec.Hash(),
		Revision:         0,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastTransitionAt: now,
		ExpiresAt:        nil,
	}
	if err := s.store.Create(ctx, sandbox); err != nil {
		return domain.Sandbox{}, fmt.Errorf("persist sandbox creation: %w", err)
	}
	return sandbox, nil
}

// Get 返回持久化的 sandbox 期望状态和最近一次观测状态。
func (s *SandboxService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	return s.store.Get(ctx, id)
}
