package runner

import (
	"context"
	"sync"
)

type Manager struct {
	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

func NewManager() *Manager {
	return &Manager{cancel: make(map[string]context.CancelFunc)}
}

func (m *Manager) Register(id string, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.cancel[id]; exists {
		return false
	}
	m.cancel[id] = cancel
	return true
}

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
