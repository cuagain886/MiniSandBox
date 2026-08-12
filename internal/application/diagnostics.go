package application

import (
	"context"
	"fmt"
	"sync"
	"time"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

// DiagnosticsStore 是 diagnostics 唯一允许使用的 Store 只读端口。
type DiagnosticsStore interface {
	ListAll(context.Context) ([]domain.Sandbox, error)
	ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error)
}

// DiagnosticsRuntime 是 runtime 安全摘要端口；实现不得返回 inspect、路径或标签。
type DiagnosticsRuntime interface {
	Diagnostics(context.Context) (RuntimeDiagnostics, error)
}

// DiagnosticsScheduler 是内存 queue/worker 安全摘要端口。
type DiagnosticsScheduler interface{ Diagnostics() SchedulerDiagnostics }

// RuntimeDiagnostics 只包含 runtime 可证明的低基数计数。
type RuntimeDiagnostics struct {
	ManagedSandboxes  int `json:"managed_sandboxes"`
	OutboundSandboxes int `json:"outbound_sandboxes"`
	DriftedSandboxes  int `json:"drifted_sandboxes"`
}

// SchedulerDiagnostics 只包含 reconcile queue 与 worker 当前计数。
type SchedulerDiagnostics struct {
	QueueDepth    int `json:"queue_depth"`
	ActiveWorkers int `json:"active_workers"`
}

// DiagnosticsSection 表达单个依赖的 available/unavailable 状态及安全数据。
type DiagnosticsSection struct {
	Status          string         `json:"status"`
	Counts          map[string]int `json:"counts,omitempty"`
	Classifications map[string]int `json:"classifications,omitempty"`
}

// DiagnosticsSnapshot 是一次有界只读聚合结果。
type DiagnosticsSnapshot struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Store       DiagnosticsSection `json:"store"`
	Runtime     DiagnosticsSection `json:"runtime"`
	Scheduler   DiagnosticsSection `json:"scheduler"`
	Anomalies   DiagnosticsSection `json:"anomalies"`
}

// DiagnosticsService 并发读取互不依赖的安全观察端口。
type DiagnosticsService struct {
	store     DiagnosticsStore
	runtime   DiagnosticsRuntime
	scheduler DiagnosticsScheduler
	timeout   time.Duration
	now       func() time.Time
}

// NewDiagnosticsService 创建每 section 使用独立 timeout 的只读服务。
func NewDiagnosticsService(store DiagnosticsStore, runtime DiagnosticsRuntime, scheduler DiagnosticsScheduler, timeout time.Duration, now func() time.Time) (*DiagnosticsService, error) {
	if store == nil || runtime == nil || scheduler == nil || timeout <= 0 || now == nil {
		return nil, fmt.Errorf("diagnostics dependencies: %w", domain.ErrInvalid)
	}
	return &DiagnosticsService{store: store, runtime: runtime, scheduler: scheduler, timeout: timeout, now: now}, nil
}

// Snapshot 聚合固定 allowlist 字段；部分失败不会阻断其他 section。
func (s *DiagnosticsService) Snapshot(ctx context.Context) DiagnosticsSnapshot {
	result := DiagnosticsSnapshot{GeneratedAt: s.now().UTC(), Store: unavailableSection(), Runtime: unavailableSection(), Scheduler: availableSection(), Anomalies: unavailableSection()}
	scheduler := s.scheduler.Diagnostics()
	result.Scheduler.Counts = map[string]int{"queue_depth": nonNegativeInt(scheduler.QueueDepth), "active_workers": nonNegativeInt(scheduler.ActiveWorkers)}
	var wait sync.WaitGroup
	var mu sync.Mutex
	wait.Add(3)
	go func() {
		defer wait.Done()
		sectionCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		records, err := s.store.ListAll(sectionCtx)
		if err != nil {
			return
		}
		counts := map[string]int{}
		for _, record := range records {
			counts[string(record.ObservedState)]++
		}
		mu.Lock()
		result.Store = DiagnosticsSection{Status: "available", Counts: counts}
		mu.Unlock()
	}()
	go func() {
		defer wait.Done()
		sectionCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		anomalies, err := s.store.ListActiveRuntimeAnomalies(sectionCtx)
		if err != nil {
			return
		}
		classes := map[string]int{}
		for _, anomaly := range anomalies {
			classes[string(anomaly.Classification)]++
		}
		mu.Lock()
		result.Anomalies = DiagnosticsSection{Status: "available", Counts: map[string]int{"active": len(anomalies)}, Classifications: classes}
		mu.Unlock()
	}()
	go func() {
		defer wait.Done()
		sectionCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		runtime, err := s.runtime.Diagnostics(sectionCtx)
		if err != nil {
			return
		}
		mu.Lock()
		result.Runtime = DiagnosticsSection{Status: "available", Counts: map[string]int{"managed_sandboxes": nonNegativeInt(runtime.ManagedSandboxes), "outbound_sandboxes": nonNegativeInt(runtime.OutboundSandboxes), "drifted_sandboxes": nonNegativeInt(runtime.DriftedSandboxes)}}
		mu.Unlock()
	}()
	wait.Wait()
	return result
}

func unavailableSection() DiagnosticsSection { return DiagnosticsSection{Status: "unavailable"} }
func availableSection() DiagnosticsSection   { return DiagnosticsSection{Status: "available"} }
func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
