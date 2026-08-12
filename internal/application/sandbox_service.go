package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
	"minisandbox/internal/testcrashpoint"
)

const (
	// createAcceptedReason 是创建意图首次持久化时的稳定机器可读原因。
	createAcceptedReason = domain.SandboxReasonCreateAccepted
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
	store         store.Store
	idGenerator   IDGenerator
	clock         Clock
	specBuilder   SandboxSpecBuilder
	waker         Waker
	allowOutbound bool
	createPolicy  CreatePolicy
}

// CreatePolicy 固定公共 create 的 TTL 与实例容量边界。
type CreatePolicy struct {
	// DefaultTTL 是请求省略 TTL 时使用的服务端租期。
	DefaultTTL time.Duration
	// MinimumTTL 和 MaximumTTL 是显式及默认 TTL 的闭区间。
	MinimumTTL time.Duration
	MaximumTTL time.Duration
	// MaxSandboxes 是非 Terminated sandbox 的事务内容量上限。
	MaxSandboxes int
}

// NewSandboxServiceWithCreatePolicy 创建完整装配 Phase 3 create 语义的生命周期服务。
func NewSandboxServiceWithCreatePolicy(
	s store.Store,
	idGenerator IDGenerator,
	clock Clock,
	specBuilder SandboxSpecBuilder,
	waker Waker,
	allowOutbound bool,
	policy CreatePolicy,
) *SandboxService {
	service := NewSandboxServiceWithOutbound(s, idGenerator, clock, specBuilder, waker, allowOutbound)
	service.createPolicy = policy
	return service
}

// NewSandboxServiceWithOutbound 创建按平台门禁允许 outbound 意图的生命周期服务。
func NewSandboxServiceWithOutbound(s store.Store, idGenerator IDGenerator, clock Clock, specBuilder SandboxSpecBuilder, waker Waker, allowOutbound bool) *SandboxService {
	service := NewSandboxService(s, idGenerator, clock, specBuilder, waker)
	service.allowOutbound = allowOutbound
	return service
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
	// P2-005 只冻结并贯通 outbound 契约；在后续配置门禁和 sidecar runtime
	// 完成前拒绝实际创建，避免把 NetworkMode=none 伪装成 outbound 成功。
	if command.Outbound && !s.allowOutbound {
		return domain.Sandbox{}, domain.ErrOutboundNotAllowed
	}
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
	if !testcrashpoint.Drop("wake.create") {
		s.waker.Wake(sandbox.ID)
	}
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

// Delete 幂等提交 DesiredTerminated，并唤醒后台删除收敛。
//
// 已经观测为 Terminated 时直接返回且不再唤醒；其他已提交终止意图的记录
// 仍通过 Store 原子抢占旧 backoff 后 Wake。首次 CAS 冲突只重读一次并最多
// 再更新一次，本方法绝不直接调用 Runtime.Delete 或修改 observed state。
func (s *SandboxService) Delete(
	ctx context.Context,
	command DeleteSandbox,
) (domain.Sandbox, error) {
	current, err := s.Get(ctx, command.SandboxID)
	if err != nil {
		return domain.Sandbox{}, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		if current.ObservedState == domain.StateTerminated {
			return current, nil
		}
		updated, err := s.store.UpdateDesired(
			ctx,
			current.ID,
			domain.DesiredTerminated,
			current.Revision,
		)
		if err == nil {
			testcrashpoint.Hit("delete.desired-cas")
			if !testcrashpoint.Drop("wake.delete") {
				s.waker.Wake(updated.ID)
			}
			return updated, nil
		}
		if !errors.Is(err, domain.ErrConflict) || attempt == 1 {
			return domain.Sandbox{}, fmt.Errorf(
				"submit sandbox termination: %w",
				err,
			)
		}

		// 冲突表示 snapshot 已过期；只重读一次，避免竞争激烈时请求线程
		// 无界自旋。重读后若其他调用已提交目标，下一轮会走幂等分支。
		current, err = s.Get(ctx, command.SandboxID)
		if err != nil {
			return domain.Sandbox{}, err
		}
	}

	panic("unreachable delete retry loop")
}
