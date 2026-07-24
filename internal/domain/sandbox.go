// Package domain 定义 MiniSandbox 的领域状态、业务不变量和领域错误。
//
// 本模块是核心依赖的最内层，不依赖 HTTP 协议、Docker SDK、SQLite driver
// 或其他基础设施实现。
package domain

import "time"

// Sandbox 保存生命周期收敛所需的期望状态、观测状态和恢复元数据。
type Sandbox struct {
	ID            string
	Image         string
	DesiredState  DesiredState
	ObservedState SandboxState
	Workspace     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ExpiresAt     *time.Time
	Revision      uint64
	FailureReason string
}

// Expired 判断 sandbox 在给定时间点是否已经达到 TTL。
func (s Sandbox) Expired(now time.Time) bool {
	return s.ExpiresAt != nil && !now.Before(*s.ExpiresAt)
}
