// Package store 定义 sandbox 期望状态的持久化端口。
//
// 本模块只规定生命周期用例需要的存取能力：创建、读取、CAS 更新和恢复
// 扫描。它不暴露 SQLite 等具体数据库类型；Phase 1 不提供物理删除记录的
// 方法，Terminated 记录保留用于查询与审计。
package store

import (
	"context"

	"minisandbox/internal/domain"
)

// ObservedUpdate 描述 reconciler 对观测状态的一次 CAS 更新请求。
type ObservedUpdate struct {
	// ID 是被更新的 sandbox 标识。
	ID string
	// ExpectedRevision 是调用方读取记录时的修订号，不匹配时更新失败。
	ExpectedRevision uint64
	// State 是最新观测到的生命周期状态。
	State domain.SandboxState
	// Reason 是与 State 对应的稳定机器可读原因。
	Reason string
	// Message 是安全的人类可读说明，不得包含秘密或宿主机路径。
	Message string
	// RuntimeID 是 runtime adapter 返回的内部资源标识，可为空。
	RuntimeID string
}

// Store 定义 sandbox 期望状态的持久化能力。
//
// 所有更新都基于 revision CAS：expectedRevision 与存量记录不一致时必须
// 返回 domain.ErrConflict 并保持记录不变，调用方重新读取后重试。
type Store interface {
	// Create 持久化一条新记录，ID 已存在时返回 domain.ErrConflict。
	Create(ctx context.Context, sandbox domain.Sandbox) error
	// Get 按 ID 返回 sandbox，不存在时返回 domain.ErrNotFound。
	Get(ctx context.Context, id string) (domain.Sandbox, error)
	// UpdateDesired 以 CAS 方式更新期望状态并返回更新后的记录。
	UpdateDesired(
		ctx context.Context,
		id string,
		desired domain.DesiredState,
		expectedRevision uint64,
	) (domain.Sandbox, error)
	// UpdateObserved 以 CAS 方式更新观测状态并返回更新后的记录。
	UpdateObserved(
		ctx context.Context,
		update ObservedUpdate,
	) (domain.Sandbox, error)
	// ListReconcileCandidates 返回最多 limit 条仍需收敛的记录。
	ListReconcileCandidates(
		ctx context.Context,
		limit int,
	) ([]domain.Sandbox, error)
	// ListAll 返回全部持久化记录，用于启动恢复和诊断。
	ListAll(ctx context.Context) ([]domain.Sandbox, error)
}
