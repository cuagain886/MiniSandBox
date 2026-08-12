package metrics

import (
	"reflect"
	"sync"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// TestTimingMetricsUseFixedBucketsAndSeconds 验证 bucket 边界、单位和 probe 分类契约。
func TestTimingMetricsUseFixedBucketsAndSeconds(t *testing.T) {
	registry := NewRegistry()
	metrics, err := NewTimingMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveCreateDuration(0.0025)
	metrics.ObserveReconcileDuration("recover", 0.025)
	metrics.ObserveRunnerProbe("healthy")
	metrics.ObserveRunnerProbe("raw-error-value")
	families, err := registry.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		switch family.GetName() {
		case "minisandbox_sandbox_create_duration_seconds":
			if !reflect.DeepEqual(histogramBounds(family), requestBuckets) {
				t.Fatalf("request buckets: %v", histogramBounds(family))
			}
		case "minisandbox_reconcile_duration_seconds":
			if !reflect.DeepEqual(histogramBounds(family), operationBuckets) {
				t.Fatalf("operation buckets: %v", histogramBounds(family))
			}
		}
		assertSafeMetricLabels(t, family)
	}
}

// TestTimingMetricsSupportConcurrentObservation 验证 histogram 与 probe counter 并发更新安全。
func TestTimingMetricsSupportConcurrentObservation(t *testing.T) {
	registry := NewRegistry()
	metrics, _ := NewTimingMetrics(registry)
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				metrics.ObserveCreateDuration(0.01)
				metrics.ObserveReconcileDuration("health", 0.1)
				metrics.ObserveRunnerProbe("unhealthy")
			}
		}()
	}
	wait.Wait()
}

func histogramBounds(family *dto.MetricFamily) []float64 {
	if len(family.Metric) == 0 || family.Metric[0].Histogram == nil {
		return nil
	}
	buckets := family.Metric[0].Histogram.Bucket
	result := make([]float64, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, bucket.GetUpperBound())
	}
	return result
}
