package runner

import (
	"errors"
	"os"
	"sort"
	"sync"
	"time"
)

// ErrCompletedExecutionCleanup 表示至少一个已驱逐 execution 的日志尚未安全删除，可在下一轮重试。
var ErrCompletedExecutionCleanup = errors.New("completed execution cleanup is pending")

type completedExecutionRecord struct {
	id          ExecutionID
	completedAt time.Time
	events      *EventStore
}

// CompletedExecutionGC 按完成时间和数量上限驱逐终态 execution，并重试受控日志删除。
type CompletedExecutionGC struct {
	manager     *Manager
	directory   string
	retention   time.Duration
	maxRetained int
	removeLog   func(string, ExecutionID) error

	mu      sync.Mutex
	pending map[ExecutionID]time.Time
}

// NewCompletedExecutionGC 创建 completed retention collector；运行中的 execution 永远不参与选择。
func NewCompletedExecutionGC(
	manager *Manager,
	directory string,
	retention time.Duration,
	maxRetained int,
) (*CompletedExecutionGC, error) {
	return newCompletedExecutionGC(manager, directory, retention, maxRetained, removeCompletedExecutionLog)
}

func newCompletedExecutionGC(
	manager *Manager,
	directory string,
	retention time.Duration,
	maxRetained int,
	removeLog func(string, ExecutionID) error,
) (*CompletedExecutionGC, error) {
	if manager == nil || retention <= 0 || maxRetained <= 0 || removeLog == nil {
		return nil, errors.New("completed execution GC is not configured")
	}
	if _, err := BackgroundLogPath(directory, "exec_validation"); err != nil {
		return nil, err
	}
	return &CompletedExecutionGC{
		manager: manager, directory: directory, retention: retention,
		maxRetained: maxRetained, removeLog: removeLog, pending: make(map[ExecutionID]time.Time),
	}, nil
}

// Run 原子地先从 Manager 查询集合移除到期/超额对象，再逐个删除日志；失败项保留到下一轮重试。
func (g *CompletedExecutionGC) Run(now time.Time) error {
	if g == nil || now.IsZero() {
		return ErrCompletedExecutionCleanup
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	records := g.manager.evictCompleted(now.UTC(), g.retention, g.maxRetained)
	for _, record := range records {
		g.pending[record.id] = record.completedAt
		if record.events != nil {
			record.events.Close()
		}
	}
	ids := make([]ExecutionID, 0, len(g.pending))
	for id := range g.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		leftTime, rightTime := g.pending[ids[left]], g.pending[ids[right]]
		if leftTime.Equal(rightTime) {
			return ids[left] < ids[right]
		}
		return leftTime.Before(rightTime)
	})
	failed := false
	for _, id := range ids {
		if err := g.removeLog(g.directory, id); err != nil {
			failed = true
			continue
		}
		delete(g.pending, id)
	}
	if failed {
		return ErrCompletedExecutionCleanup
	}
	return nil
}

func (m *Manager) evictCompleted(now time.Time, retention time.Duration, maxRetained int) []completedExecutionRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make([]completedExecutionRecord, 0)
	for id, entry := range m.executions {
		if entry.active || entry.completedAt.IsZero() || !terminalExecutionState(entry.execution.Descriptor().State) {
			continue
		}
		records = append(records, completedExecutionRecord{id: id, completedAt: entry.completedAt, events: entry.events})
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].completedAt.Equal(records[right].completedAt) {
			return records[left].id < records[right].id
		}
		return records[left].completedAt.Before(records[right].completedAt)
	})
	countEvictions := len(records) - maxRetained
	if countEvictions < 0 {
		countEvictions = 0
	}
	cutoff := now.Add(-retention)
	evicted := make([]completedExecutionRecord, 0)
	for index, record := range records {
		if index >= countEvictions && record.completedAt.After(cutoff) {
			continue
		}
		delete(m.executions, record.id)
		evicted = append(evicted, record)
	}
	return evicted
}

func removeCompletedExecutionLog(directory string, id ExecutionID) error {
	path, err := BackgroundLogPath(directory, id)
	if err != nil {
		return ErrCompletedExecutionCleanup
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrCompletedExecutionCleanup
	}
	if err := os.Remove(path); err != nil {
		return ErrCompletedExecutionCleanup
	}
	return nil
}
