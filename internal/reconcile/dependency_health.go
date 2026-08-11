package reconcile

import (
	"context"
	"errors"
	"sync"
	"time"

	runtimeport "minisandbox/internal/runtime"
)

// DependencyProbe 是 Store 与 Docker 的轻量只读健康探测端口。
type DependencyProbe interface {
	// ProbeDependency 必须服从调用方 deadline，且不得改变 sandbox 资源状态。
	ProbeDependency(context.Context) error
}

// DependencyReadiness 接收依赖 freshness 推导出的全局 readiness。
type DependencyReadiness interface {
	SetStore(bool)
	SetDocker(bool)
}

// DependencyErrorClass 是不会泄露底层连接信息的探测结果类别。
type DependencyErrorClass string

const (
	// DependencyErrorNone 表示最近一次探测成功。
	DependencyErrorNone DependencyErrorClass = "none"
	// DependencyErrorTimeout 表示探测超过独立 deadline。
	DependencyErrorTimeout DependencyErrorClass = "timeout"
	// DependencyErrorUnavailable 表示依赖明确报告暂时不可用。
	DependencyErrorUnavailable DependencyErrorClass = "unavailable"
	// DependencyErrorInternal 表示未分类的安全内部故障。
	DependencyErrorInternal DependencyErrorClass = "internal"
)

// DependencyHealthSnapshot 是不包含原始错误的监测快照。
type DependencyHealthSnapshot struct {
	// StoreLastSuccess 与 DockerLastSuccess 是最近成功探测的 UTC 时间。
	StoreLastSuccess  time.Time
	DockerLastSuccess time.Time
	// StoreError 与 DockerError 是最近探测的安全类别。
	StoreError  DependencyErrorClass
	DockerError DependencyErrorClass
}

// DependencyHealthMonitor 独立探测 Store/Docker 并按 freshness 驱动全局状态。
type DependencyHealthMonitor struct {
	store, docker DependencyProbe
	readiness     DependencyReadiness
	gate          runtimeport.OperationAvailability
	clock         Clock
	interval      time.Duration
	timeout       time.Duration
	freshness     time.Duration
	report        ErrorReporter

	mu       sync.RWMutex
	snapshot DependencyHealthSnapshot
}

// NewDependencyHealthMonitor 创建从“启动探测刚成功”状态开始的监测器。
func NewDependencyHealthMonitor(storeProbe, dockerProbe DependencyProbe, readiness DependencyReadiness, gate runtimeport.OperationAvailability, clock Clock, interval, timeout, freshness time.Duration, report ErrorReporter) (*DependencyHealthMonitor, error) {
	if storeProbe == nil || dockerProbe == nil || readiness == nil || gate == nil || clock == nil || interval <= 0 || timeout <= 0 || freshness < timeout {
		return nil, errors.New("invalid dependency health monitor configuration")
	}
	now := clock.Now().UTC()
	return &DependencyHealthMonitor{
		store: storeProbe, docker: dockerProbe, readiness: readiness, gate: gate,
		clock: clock, interval: interval, timeout: timeout, freshness: freshness, report: report,
		snapshot: DependencyHealthSnapshot{
			StoreLastSuccess: now, DockerLastSuccess: now,
			StoreError: DependencyErrorNone, DockerError: DependencyErrorNone,
		},
	}, nil
}

// ProbeOnce 并发执行两个带独立 timeout 的探测并更新 freshness 状态。
func (m *DependencyHealthMonitor) ProbeOnce(ctx context.Context) {
	type result struct {
		docker bool
		err    error
	}
	results := make(chan result, 2)
	probe := func(docker bool, dependency DependencyProbe) {
		probeCtx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()
		results <- result{docker: docker, err: dependency.ProbeDependency(probeCtx)}
	}
	go probe(false, m.store)
	go probe(true, m.docker)
	for range 2 {
		outcome := <-results
		m.record(outcome.docker, outcome.err, m.clock.Now().UTC())
	}
}

// Run 启动即探测，随后按固定周期运行直到 shutdown。
func (m *DependencyHealthMonitor) Run(ctx context.Context) {
	m.ProbeOnce(ctx)
	ticker := m.clock.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			m.ProbeOnce(ctx)
		}
	}
}

// Snapshot 返回不含原始 cause 的并发安全快照。
func (m *DependencyHealthMonitor) Snapshot() DependencyHealthSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

func (m *DependencyHealthMonitor) record(docker bool, err error, now time.Time) {
	m.mu.Lock()
	if docker {
		if err == nil {
			m.snapshot.DockerLastSuccess, m.snapshot.DockerError = now, DependencyErrorNone
			m.readiness.SetDocker(true)
			m.gate.SetAvailable(true)
			m.mu.Unlock()
			return
		}
		m.snapshot.DockerError = classifyDependencyError(err)
		if !now.Before(m.snapshot.DockerLastSuccess.Add(m.freshness)) {
			m.readiness.SetDocker(false)
			m.gate.SetAvailable(false)
		}
	} else {
		if err == nil {
			m.snapshot.StoreLastSuccess, m.snapshot.StoreError = now, DependencyErrorNone
			m.readiness.SetStore(true)
			m.mu.Unlock()
			return
		}
		m.snapshot.StoreError = classifyDependencyError(err)
		if !now.Before(m.snapshot.StoreLastSuccess.Add(m.freshness)) {
			m.readiness.SetStore(false)
		}
	}
	m.mu.Unlock()
	if m.report != nil {
		func() {
			defer func() { _ = recover() }()
			m.report(errors.New("dependency health probe failed"))
		}()
	}
}

func classifyDependencyError(err error) DependencyErrorClass {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return DependencyErrorTimeout
	}
	var unavailable interface{ Unavailable() bool }
	if errors.As(err, &unavailable) && unavailable.Unavailable() {
		return DependencyErrorUnavailable
	}
	return DependencyErrorInternal
}
