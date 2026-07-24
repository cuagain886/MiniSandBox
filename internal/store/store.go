// Package store 定义 sandbox 期望状态和幂等信息的持久化端口。
//
// 本模块只规定领域层需要的存取能力，不能暴露 SQLite 等具体数据库类型。
package store

import (
	"context"

	"minisandbox/internal/domain"
)

// Store 定义 sandbox 期望状态的持久化能力。
type Store interface {
	// Save 原子保存 sandbox 规格、状态和修订号。
	Save(context.Context, domain.Sandbox) error
	// Get 按 ID 返回 sandbox，不存在时返回 domain.ErrNotFound。
	Get(context.Context, string) (domain.Sandbox, error)
	// List 返回需要恢复或继续收敛的 sandbox 集合。
	List(context.Context) ([]domain.Sandbox, error)
	// Delete 删除已确认清理完成的持久化记录，并保持幂等。
	Delete(context.Context, string) error
}
