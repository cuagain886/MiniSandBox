package runner

import (
	"errors"
	"sync"
)

var (
	// ErrExecutionLimitReached 是并发槽位耗尽时返回给上层映射的稳定错误码。
	ErrExecutionLimitReached = errors.New("EXECUTION_LIMIT_REACHED")
	// ErrExecutionAlreadyRegistered 表示 factory 产生了已被当前 runner 使用的 execution ID。
	ErrExecutionAlreadyRegistered = errors.New("execution already registered")
	// ErrExecutionNotFound 表示 ID 不属于当前 runner，不能跨 sandbox 查询。
	ErrExecutionNotFound = errors.New("execution not found")
	// ErrExecutionStillActive 表示调用方试图在 execution 进入终态前释放并发槽位。
	ErrExecutionStillActive = errors.New("execution is still active")
)

type executionCreator interface {
	New() (*Execution, error)
}

type managedExecution struct {
	execution *Execution
	active    bool
}

// Manager 保存单个 runner 内的 execution 注册表和并发槽位；查询只返回不可反向修改内部状态的快照。
// 已进入终态的记录继续保留，清理策略由后续 retention 任务负责。
type Manager struct {
	mu            sync.RWMutex
	maxConcurrent int
	factory       executionCreator
	executions    map[ExecutionID]*managedExecution
	active        int
}

// NewManager 创建单 runner execution manager；maxConcurrent 必须为正数。
func NewManager(maxConcurrent int) (*Manager, error) {
	return newManager(maxConcurrent, NewExecutionFactory())
}

func newManager(maxConcurrent int, factory executionCreator) (*Manager, error) {
	if maxConcurrent <= 0 || factory == nil {
		return nil, errors.New("execution manager is not configured")
	}
	return &Manager{
		maxConcurrent: maxConcurrent,
		factory:       factory,
		executions:    make(map[ExecutionID]*managedExecution),
	}, nil
}

// CreateExecution 原子占用并发槽位并注册一个 Pending execution。
// factory 失败或 ID 冲突时会在返回前释放槽位，不会留下不可查询的半注册记录。
func (m *Manager) CreateExecution() (*Execution, error) {
	if m == nil {
		return nil, errors.New("execution manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active >= m.maxConcurrent {
		return nil, ErrExecutionLimitReached
	}
	// 先占槽再调用 factory，使并发请求不能同时越过上限判断。
	m.active++
	execution, err := m.factory.New()
	if err != nil {
		m.active--
		return nil, err
	}
	if execution == nil || execution.Descriptor().State != ExecutionPending {
		m.active--
		return nil, errors.New("execution factory returned invalid execution")
	}
	id := execution.Descriptor().ID
	if id == "" {
		m.active--
		return nil, errors.New("execution factory returned empty ID")
	}
	if _, exists := m.executions[id]; exists {
		m.active--
		return nil, ErrExecutionAlreadyRegistered
	}
	m.executions[id] = &managedExecution{execution: execution, active: true}
	return execution, nil
}

// Descriptor 返回指定 execution 的当前值快照；调用方修改快照不会改变 manager 内部记录。
func (m *Manager) Descriptor(id ExecutionID) (ExecutionDescriptor, error) {
	if m == nil {
		return ExecutionDescriptor{}, ErrExecutionNotFound
	}
	m.mu.RLock()
	entry, exists := m.executions[id]
	m.mu.RUnlock()
	if !exists {
		return ExecutionDescriptor{}, ErrExecutionNotFound
	}
	return entry.execution.Descriptor(), nil
}

// Complete 在 execution 已进入终态后释放一次并发槽位，同时保留其描述符供后续查询。
// 重复调用保持幂等；Pending 或 Running 不能伪装为已完成。
func (m *Manager) Complete(id ExecutionID) error {
	if m == nil {
		return ErrExecutionNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, exists := m.executions[id]
	if !exists {
		return ErrExecutionNotFound
	}
	if !terminalExecutionState(entry.execution.Descriptor().State) {
		return ErrExecutionStillActive
	}
	if entry.active {
		entry.active = false
		m.active--
	}
	return nil
}

func terminalExecutionState(state ExecutionState) bool {
	switch state {
	case ExecutionExited, ExecutionFailed, ExecutionCancelled, ExecutionTimedOut:
		return true
	default:
		return false
	}
}
