// Package domain 定义 MiniSandbox 的领域状态、业务不变量和领域错误。
//
// 本模块是核心依赖的最内层，不依赖 HTTP 协议、Docker SDK、SQLite driver
// 或其他基础设施实现。
package domain

import "time"

// SandboxOrigin 是 sandbox record 的受控来源，不包含任意外部标识。
type SandboxOrigin string

const (
	// SandboxOriginAPI 表示 record 来自公共 create 请求或旧 schema 回填。
	SandboxOriginAPI SandboxOrigin = "api"
	// SandboxOriginRecoveredOrphan 表示 record 来自完整可信 orphan bundle 导入。
	SandboxOriginRecoveredOrphan SandboxOrigin = "recovered_orphan"
)

// Sandbox 保存生命周期收敛所需的期望状态、观测状态和恢复元数据。
type Sandbox struct {
	// ID 是控制面生成并持久化的稳定 sandbox 标识。
	ID string
	// Spec 是创建时已经解析完成、可用于重启恢复的完整运行规格。
	Spec SandboxSpec
	// DesiredState 是 Store 中由 API 请求提交的目标状态。
	DesiredState DesiredState
	// ObservedState 是 reconciler 最近一次持久化的观测状态。
	ObservedState SandboxState
	// Reason 是与 ObservedState 对应的稳定机器可读原因。
	Reason string
	// Message 是安全的人类可读说明，不得包含秘密、宿主机路径或内部堆栈。
	Message string
	// RuntimeID 是 runtime adapter 返回的内部资源标识，不得直接暴露给公共 API。
	RuntimeID string
	// SpecHash 是 resolved spec 的稳定哈希，用于恢复时识别资源漂移。
	SpecHash string
	// Revision 是 Store CAS 使用的单调递增修订号，未持久化记录的零值为 0。
	Revision uint64
	// CreatedAt 是创建意图首次持久化的时间，由应用层统一转换为 UTC。
	CreatedAt time.Time
	// UpdatedAt 是记录最近一次持久化更新的时间，由应用层统一转换为 UTC。
	UpdatedAt time.Time
	// LastTransitionAt 是 ObservedState 最近一次变化的时间，由应用层统一转换为 UTC。
	LastTransitionAt time.Time
	// ExpiresAt 是 Store 权威租约的 UTC 到期时间；已持久化记录必须非 nil。
	ExpiresAt *time.Time
	// RetryAttempt 是当前持久化 retryable failure 序号，CAS conflict 不增加。
	RetryAttempt uint32
	// NextReconcileAt 是下一次允许尝试收敛的 UTC 时间；未调度时为 nil。
	NextReconcileAt *time.Time
	// LastReconcileAt 是最近一次完成 reconcile attempt 的 UTC 时间；尚未执行时为 nil。
	LastReconcileAt *time.Time
	// HealthFailureCount 是连续 runner health probe 失败次数，成功后归零。
	HealthFailureCount uint32
	// Origin 是 record 的固定来源分类。
	Origin SandboxOrigin
}

// Expired 判断 sandbox 在给定时间点是否已经达到预留的 Phase 3 TTL。
func (s Sandbox) Expired(now time.Time) bool {
	return s.ExpiresAt != nil && !now.Before(*s.ExpiresAt)
}
