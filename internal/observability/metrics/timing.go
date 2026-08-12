package metrics

import "github.com/prometheus/client_golang/prometheus"

var requestBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

var operationBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

var probeResults = enumSet("healthy", "unhealthy", "error")

// TimingMetrics 聚合 create/reconcile duration 与真实 runner probe 结果。
type TimingMetrics struct {
	create    prometheus.Histogram
	reconcile *prometheus.HistogramVec
	probe     *prometheus.CounterVec
}

// NewTimingMetrics 构造并注册固定 bucket 的 P3-085 collectors。
func NewTimingMetrics(registry *Registry) (*TimingMetrics, error) {
	metrics := &TimingMetrics{
		create:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "minisandbox_sandbox_create_duration_seconds", Help: "Control-plane create request duration in seconds.", Buckets: append([]float64(nil), requestBuckets...)}),
		reconcile: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "minisandbox_reconcile_duration_seconds", Help: "Completed reconcile attempt duration in seconds.", Buckets: append([]float64(nil), operationBuckets...)}, []string{"operation"}),
		probe:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "minisandbox_runner_probe_total", Help: "Actual runner health probes completed by result."}, []string{"result"}),
	}
	for _, collector := range []prometheus.Collector{metrics.create, metrics.reconcile, metrics.probe} {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

// ObserveCreateDuration 记录非负 create 请求秒数；负值归零避免产生无意义样本。
func (m *TimingMetrics) ObserveCreateDuration(seconds float64) {
	m.create.Observe(nonNegative(seconds))
}

// ObserveReconcileDuration 记录固定 operation 的 reconcile 秒数。
func (m *TimingMetrics) ObserveReconcileDuration(operation string, seconds float64) {
	m.reconcile.WithLabelValues(normalize(operation, reconcileOperations)).Observe(nonNegative(seconds))
}

// ObserveRunnerProbe 记录一次真实 runner probe 完成分类。
func (m *TimingMetrics) ObserveRunnerProbe(result string) {
	m.probe.WithLabelValues(normalize(result, probeResults)).Inc()
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
