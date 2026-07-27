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

// Waker 提醒后台 reconciler 尽快处理指定 sandbox。
//
// Wake 是无业务状态的尽力通知，允许在进程关闭时静默丢弃；Store 中的持久化
// 记录才是事实源，启动恢复会重新扫描未收敛记录。
type Waker interface {
	// Wake 尝试唤醒 sandbox ID，不保证通知实际进入内存队列。
	Wake(id string)
}

// SandboxService 编排 sandbox 生命周期用例及其持久化访问。
type SandboxService struct {
	store       store.Store
	idGenerator IDGenerator
	clock       Clock
	specBuilder SandboxSpecBuilder
	waker       Waker
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
	waker Waker,
) *SandboxService {
	return &SandboxService{
		store:       s,
		idGenerator: idGenerator,
		clock:       clock,
		specBuilder: specBuilder,
		waker:       waker,
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
	// Wake 只能出现在持久化成功之后；它没有返回值，防止队列关闭把已经落库
	// 的创建意图改写为客户端失败并诱发重复创建。
	s.waker.Wake(sandbox.ID)
	return sandbox, nil
}

// Get 按 ID 返回 Store 中的 sandbox 期望状态和最近一次观测状态。
//
// 当前单租户版本不增加权限规则；本方法不读取 Runtime。Store 错误保留
// errors.Is 分类并返回零值 sandbox，供后续 HTTP mapper 统一处理。
func (s *SandboxService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	sandbox, err := s.store.Get(ctx, id)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("get sandbox: %w", err)
	}
	return sandbox, nil
}
