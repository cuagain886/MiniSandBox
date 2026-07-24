package runner

import (
	"context"
	"sync"
)

// Manager 跟踪当前 runner 启动的执行及其取消函数。
type Manager struct {
	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

// NewManager 创建不包含活动执行的管理器。
func NewManager() *Manager {
	return &Manager{cancel: make(map[string]context.CancelFunc)}
}

// Register 注册执行的取消函数；ID 已存在时返回 false，防止覆盖活动执行。
func (m *Manager) Register(id string, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.cancel[id]; exists {
		return false
	}
	m.cancel[id] = cancel
	return true
}

// Cancel 移除并调用指定执行的取消函数；执行不存在或已结束时返回 false。
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	cancel, exists := m.cancel[id]
	if exists {
		delete(m.cancel, id)
	}
	m.mu.Unlock()
	if exists {
		cancel()
	}
	return exists
}
