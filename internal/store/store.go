// Package store 定义 sandbox 期望状态的持久化端口。
//
// 本模块只规定生命周期用例需要的存取能力：创建、读取、CAS 更新和恢复
// 扫描。它不暴露 SQLite 等具体数据库类型；Phase 1 不提供物理删除记录的
// 方法，Terminated 记录保留用于查询与审计。
package store

import (
	"context"
	"errors"
	"time"

	"minisandbox/internal/domain"
)

// ReconcileCandidateQuery 描述一次有界、可续页的 due candidate 查询。
type ReconcileCandidateQuery struct {
	// Now 是判断 expiry 和持久化 retry 是否到期的 UTC 时间。
	Now time.Time
	// RunningCutoff 是 Running record 的 last_reconcile_at 截止时间。
	RunningCutoff time.Time
	// AfterID 是上一页最后一个 sandbox ID；空字符串表示第一页。
	AfterID string
	// Limit 是本页最多返回的记录数，必须为正数。
	Limit int
}

// IdempotencyGCQuery 描述一次有界的终态幂等记录回收事务。
type IdempotencyGCQuery struct {
	// Now 是 Store 判断终态保留期的权威 UTC 时间。
	Now time.Time
	// TerminalRetention 是 Terminated 后的最短保留期，不得小于 24 小时。
	TerminalRetention time.Duration
	// AfterScopeID 和 AfterKey 组成上一批最后一个稳定 key；均为空表示首批。
	AfterScopeID string
	AfterKey     string
	// Limit 是本批最多删除的记录数，必须为正数。
	Limit int
}

// IdempotencyGCBatch 描述一次原子回收的结果和下一页游标。
type IdempotencyGCBatch struct {
	// Deleted 是本事务实际删除的幂等记录数。
	Deleted int
	// LastScopeID 和 LastKey 是本批最后处理的稳定 key；空批保持为空。
	LastScopeID string
	LastKey     string
}

// ErrCorrupt 表示持久化记录无法安全还原为领域对象。
//
// 调用方应把它视为需要运维介入的数据完整性故障，不能按 NotFound 或 CAS
// 冲突重试；具体 adapter 的错误不得回显损坏字段值或数据库内部信息。
var ErrCorrupt = errors.New("store corruption")

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

// RenewUpdate 描述一次只能延长有效 Running 租约的 CAS 更新。
type RenewUpdate struct {
	// ID 是目标 sandbox 标识。
	ID string
	// ExpectedRevision 是读取租约时观察到的 revision。
	ExpectedRevision uint64
	// Now 是服务端校验旧租约仍未到期及提前 reconcile 的 UTC 时间。
	Now time.Time
	// ExpiresAt 是严格晚于当前值的新 UTC 到期时间。
	ExpiresAt time.Time
}

// ExpireIntentUpdate 描述由当前租约 timer 提交的终止意图。
type ExpireIntentUpdate struct {
	// ID 是目标 sandbox 标识。
	ID string
	// ExpectedRevision 是提交终止意图时读取到的 revision。
	ExpectedRevision uint64
	// ExpectedExpiresAt 是 timer 携带的租约身份，必须与 Store 当前值完全相同。
	ExpectedExpiresAt time.Time
	// Now 是确认租约已经到期并提前 reconcile 的 UTC 时间。
	Now time.Time
}

// RetryUpdate 描述一次失败 reconcile 的原子持久化结果。
type RetryUpdate struct {
	// ID 是目标 sandbox 标识。
	ID string
	// ExpectedRevision 是本次 reconcile 读取到的 revision。
	ExpectedRevision uint64
	// AttemptedAt 是本次尝试完成的 UTC 时间。
	AttemptedAt time.Time
	// NextReconcileAt 是策略选定且严格晚于 AttemptedAt 的下次 UTC 时间。
	NextReconcileAt time.Time
	// Reason 是安全、稳定且机器可读的失败原因。
	Reason string
	// Message 是不含秘密和底层错误文本的人类可读说明。
	Message string
}

// RetryResetUpdate 描述一次成功收敛及 retry metadata 清零的原子结果。
type RetryResetUpdate struct {
	// Observed 是本次成功收敛要持久化的观测状态和 runtime identity。
	Observed ObservedUpdate
	// ReconciledAt 是本次尝试成功完成的 UTC 时间。
	ReconciledAt time.Time
}

// HealthResultUpdate 描述一次 Running runner health probe 的 CAS 结果。
type HealthResultUpdate struct {
	// ID 是目标 sandbox 标识。
	ID string
	// ExpectedRevision 是 probe 开始前读取到的 revision。
	ExpectedRevision uint64
	// CheckedAt 是 probe 完成的 UTC 时间。
	CheckedAt time.Time
	// Healthy 为 true 时连续失败计数归零，否则饱和递增一。
	Healthy bool
}

// IdempotentResponse 是首次接受创建时需要逐字节保存的 HTTP 响应。
type IdempotentResponse struct {
	// SchemaVersion 是 Store 支持的重放结构版本，当前固定为 1。
	SchemaVersion uint32
	// StatusCode 是首次接受状态，当前只允许 202。
	StatusCode int
	// Location 是首次响应的稳定资源路径。
	Location string
	// Body 是首次响应 JSON bytes，最大 64 KiB；调用双方不得复用其 backing array。
	Body []byte
	// CreatedAt 是首次提交记录的 UTC 时间，不因重放而变化。
	CreatedAt time.Time
}

// IdempotentCreateRequest 描述 sandbox 与重放记录的一次原子创建。
type IdempotentCreateRequest struct {
	// ScopeID 是已经校验的租户作用域。
	ScopeID string
	// Key 是已经校验且禁止记录到日志的 raw idempotency key。
	Key string
	// RequestHash 是带域分离的 lowercase SHA-256 hex。
	RequestHash string
	// Sandbox 是待创建的完整领域记录，必须包含绝对 expiry。
	Sandbox domain.Sandbox
	// Response 是与 Sandbox 同事务保存的首次 202 响应。
	Response IdempotentResponse
	// MaxSandboxes 是本次事务允许的非 Terminated 记录上限，必须为正数。
	MaxSandboxes int
}

// NonIdempotentCreateRequest 描述无 key 创建及其事务内容量上限。
type NonIdempotentCreateRequest struct {
	// Sandbox 是待创建的完整初始领域记录。
	Sandbox domain.Sandbox
	// MaxSandboxes 是本次事务允许的非 Terminated 记录上限，必须为正数。
	MaxSandboxes int
}

// IdempotentCreateResult 描述首次创建或后续精确重放结果。
type IdempotentCreateResult struct {
	// Sandbox 是首次创建后的 domain snapshot；重放时至少包含稳定 ID。
	Sandbox domain.Sandbox
	// Response 是首次保存的不可变响应副本。
	Response IdempotentResponse
	// Replayed 表示本次没有创建新 sandbox，而是命中已有相同请求。
	Replayed bool
}

// Store 定义 sandbox 期望状态的持久化能力。
//
// 所有更新都基于 revision CAS：expectedRevision 与存量记录不一致时必须
// 返回 domain.ErrConflict 并保持记录不变，调用方重新读取后重试；唯一例外是
// UpdateDesired 的目标已经满足，此时按幂等 no-op 返回当前记录。
type Store interface {
	// Create 持久化一条新记录，ID 已存在时返回 domain.ErrConflict。
	Create(ctx context.Context, sandbox domain.Sandbox) error
	// CreateNonIdempotent 在独占事务中创建一条无 key sandbox，不写重放表。
	CreateNonIdempotent(ctx context.Context, request NonIdempotentCreateRequest) (domain.Sandbox, error)
	// CreateIdempotent 在一个 BEGIN IMMEDIATE 事务中创建 sandbox 和首次响应记录。
	CreateIdempotent(ctx context.Context, request IdempotentCreateRequest) (IdempotentCreateResult, error)
	// Get 按 ID 返回 sandbox，不存在时返回 domain.ErrNotFound。
	Get(ctx context.Context, id string) (domain.Sandbox, error)
	// UpdateDesired 以 CAS 方式提交 DesiredTerminated 并返回更新后的记录。
	//
	// 已经处于 DesiredTerminated 时返回当前记录作为幂等 no-op，不递增
	// revision；只有实际状态转换才要求 expectedRevision 匹配。
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
	// Renew 以 CAS 延长尚未到期且 desired/observed 均未终止的租约。
	Renew(ctx context.Context, update RenewUpdate) (domain.Sandbox, error)
	// ExpireIntent 仅在 revision、当前 expiry 和到期时间同时匹配时提交终止意图。
	ExpireIntent(ctx context.Context, update ExpireIntentUpdate) (domain.Sandbox, error)
	// ScheduleRetry 原子记录失败结果、自增 attempt 并保存选定的下次时间。
	ScheduleRetry(ctx context.Context, update RetryUpdate) (domain.Sandbox, error)
	// ResetRetry 原子记录成功观测结果并清空旧 retry metadata。
	ResetRetry(ctx context.Context, update RetryResetUpdate) (domain.Sandbox, error)
	// RecordHealthResult 原子记录 Running probe 时间及连续失败计数。
	RecordHealthResult(ctx context.Context, update HealthResultUpdate) (domain.Sandbox, error)
	// ListReconcileCandidates 返回一页在 query 时间边界上真正 due 的记录。
	//
	// 结果严格按 ID 递增，不使用 OFFSET；调用方用最后一个 ID 继续下一页。
	// scanner cursor 不是事实源，下一轮必须从空 cursor 重新开始。
	ListReconcileCandidates(
		ctx context.Context,
		query ReconcileCandidateQuery,
	) ([]domain.Sandbox, error)
	// ListAll 返回全部持久化记录，用于启动恢复和诊断。
	ListAll(ctx context.Context) ([]domain.Sandbox, error)
}
