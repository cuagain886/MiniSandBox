// Package sqlite 承载 store 端口的 SQLite 持久化 adapter。
//
// 本模块设计上负责 schema 迁移和事务存取；当前仅提供接口骨架和初始 schema。
// 它不决定生命周期状态转换、鉴权或 runtime 行为。
package sqlite

import (
	"context"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// Store 是 store 端口的 SQLite 实现。
type Store struct {
	path string
}

// Open 打开指定 SQLite 数据库并准备 schema。
//
// 当前初始化骨架只保存路径，尚未连接数据库。
func Open(path string) (*Store, error) {
	return &Store{path: path}, nil
}

// Save 原子写入 sandbox 记录。
func (s *Store) Save(context.Context, domain.Sandbox) error {
	return domain.ErrNotImplemented
}

// Get 按 ID 读取 sandbox 记录。
func (s *Store) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, domain.ErrNotImplemented
}

// List 返回需要生命周期恢复的全部 sandbox 记录。
func (s *Store) List(context.Context) ([]domain.Sandbox, error) {
	return nil, domain.ErrNotImplemented
}

// Delete 幂等删除已经完成资源清理的 sandbox 记录。
func (s *Store) Delete(context.Context, string) error {
	return domain.ErrNotImplemented
}

var _ storeport.Store = (*Store)(nil)
