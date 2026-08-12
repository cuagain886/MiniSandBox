package metrics

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

var metricStates = []string{"Creating", "Running", "Terminating", "Terminated", "Failed"}

// SnapshotSource 是后台 sampler 唯一允许调用的 Store 只读端口。
type SnapshotSource interface {
	ListAll(context.Context) ([]domain.Sandbox, error)
	ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error)
}

type gaugeSnapshot struct {
	states             map[string]float64
	cleanup, anomalies float64
	at                 time.Time
}

// SnapshotGauges 从原子不可变快照采集 Store 状态，并直接接收 queue/worker 的内存快照。
type SnapshotGauges struct {
	snapshot                                                               atomic.Pointer[gaugeSnapshot]
	queue                                                                  atomic.Int64
	workers                                                                atomic.Int64
	now                                                                    func() time.Time
	descState, descCleanup, descAnomalies, descQueue, descWorkers, descAge *prometheus.Desc
}

// NewSnapshotGauges 注册无 Store 副作用的 gauges collector。
func NewSnapshotGauges(registry *Registry, now func() time.Time) (*SnapshotGauges, error) {
	if registry == nil || now == nil {
		return nil, fmt.Errorf("snapshot gauges dependencies are invalid")
	}
	g := &SnapshotGauges{now: now,
		descState:     prometheus.NewDesc("minisandbox_sandbox_state_count", "Sandboxes in each stable observed state from the last Store snapshot.", []string{"state"}, nil),
		descCleanup:   prometheus.NewDesc("minisandbox_cleanup_pending", "Sandboxes awaiting mandatory cleanup from the last Store snapshot.", nil, nil),
		descAnomalies: prometheus.NewDesc("minisandbox_active_anomalies", "Active runtime anomalies from the last Store snapshot.", nil, nil),
		descQueue:     prometheus.NewDesc("minisandbox_reconcile_queue_depth", "Current deduplicated reconcile queue depth.", nil, nil),
		descWorkers:   prometheus.NewDesc("minisandbox_reconcile_active_workers", "Current active reconcile workers.", nil, nil),
		descAge:       prometheus.NewDesc("minisandbox_metrics_snapshot_age_seconds", "Age of the last successful Store-backed metrics snapshot.", nil, nil)}
	if err := registry.Register(g); err != nil {
		return nil, err
	}
	return g, nil
}

// Describe 实现 prometheus.Collector 的固定 descriptor 集合。
func (g *SnapshotGauges) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{g.descState, g.descCleanup, g.descAnomalies, g.descQueue, g.descWorkers, g.descAge} {
		ch <- desc
	}
}

// Collect 只读取内存快照；尚无成功 Store snapshot 时省略 Store-backed samples 与 age。
func (g *SnapshotGauges) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(g.descQueue, prometheus.GaugeValue, float64(g.queue.Load()))
	ch <- prometheus.MustNewConstMetric(g.descWorkers, prometheus.GaugeValue, float64(g.workers.Load()))
	if snapshot := g.snapshot.Load(); snapshot != nil {
		for _, state := range metricStates {
			ch <- prometheus.MustNewConstMetric(g.descState, prometheus.GaugeValue, snapshot.states[state], state)
		}
		ch <- prometheus.MustNewConstMetric(g.descCleanup, prometheus.GaugeValue, snapshot.cleanup)
		ch <- prometheus.MustNewConstMetric(g.descAnomalies, prometheus.GaugeValue, snapshot.anomalies)
		age := g.now().Sub(snapshot.at).Seconds()
		if age < 0 {
			age = 0
		}
		ch <- prometheus.MustNewConstMetric(g.descAge, prometheus.GaugeValue, age)
	}
}

// UpdateScheduler 原子发布当前 queue/worker 数，不接收 sandbox 身份。
func (g *SnapshotGauges) UpdateScheduler(queueDepth, activeWorkers int) {
	if queueDepth < 0 {
		queueDepth = 0
	}
	if activeWorkers < 0 {
		activeWorkers = 0
	}
	g.queue.Store(int64(queueDepth))
	g.workers.Store(int64(activeWorkers))
}

// UpdateQueueDepth 原子发布当前待处理队列深度；负值按零处理。
func (g *SnapshotGauges) UpdateQueueDepth(queueDepth int) {
	if queueDepth < 0 {
		queueDepth = 0
	}
	g.queue.Store(int64(queueDepth))
}

// WorkerStarted 原子记录一个 reconcile worker 开始处理任务。
func (g *SnapshotGauges) WorkerStarted() { g.workers.Add(1) }

// WorkerFinished 原子记录一个 reconcile worker 完成任务，且不会把计数降为负数。
func (g *SnapshotGauges) WorkerFinished() {
	for {
		current := g.workers.Load()
		if current <= 0 || g.workers.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// SchedulerSnapshot 返回 diagnostics 可安全读取的内存计数，不触发 Store I/O。
func (g *SnapshotGauges) SchedulerSnapshot() (queueDepth, activeWorkers int) {
	if g == nil {
		return 0, 0
	}
	return int(g.queue.Load()), int(g.workers.Load())
}

// SampleStore 在独立 timeout 内生成完整快照；失败或超限保留旧值。
func (g *SnapshotGauges) SampleStore(ctx context.Context, source SnapshotSource, timeout time.Duration, maxRows int) error {
	if source == nil || timeout <= 0 || maxRows <= 0 {
		return fmt.Errorf("metrics snapshot sampling configuration is invalid")
	}
	sampleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	records, err := source.ListAll(sampleCtx)
	if err != nil {
		return err
	}
	if len(records) > maxRows {
		return fmt.Errorf("metrics sandbox snapshot exceeds row limit")
	}
	anomalies, err := source.ListActiveRuntimeAnomalies(sampleCtx)
	if err != nil {
		return err
	}
	if len(anomalies) > maxRows {
		return fmt.Errorf("metrics anomaly snapshot exceeds row limit")
	}
	values := make(map[string]float64, len(metricStates))
	cleanup := float64(0)
	for _, record := range records {
		state := metricState(record.ObservedState)
		values[state]++
		if record.Reason == domain.SandboxReasonCleanupPending {
			cleanup++
		}
	}
	g.snapshot.Store(&gaugeSnapshot{states: values, cleanup: cleanup, anomalies: float64(len(anomalies)), at: g.now().UTC()})
	return nil
}

func metricState(state domain.SandboxState) string {
	switch state {
	case domain.StateCreating, domain.StatePending:
		return "Creating"
	case domain.StateRunning:
		return "Running"
	case domain.StateStopping:
		return "Terminating"
	case domain.StateTerminated:
		return "Terminated"
	case domain.StateFailed:
		return "Failed"
	default:
		return "Failed"
	}
}
