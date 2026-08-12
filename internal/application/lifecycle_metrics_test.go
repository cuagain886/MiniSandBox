package application

import (
	"context"
	"testing"

	"minisandbox/internal/domain"
)

type lifecycleMetricsFake struct {
	results   []string
	durations []float64
}

func (m *lifecycleMetricsFake) ObserveCreate(result string) { m.results = append(m.results, result) }
func (m *lifecycleMetricsFake) ObserveCreateDuration(seconds float64) {
	m.durations = append(m.durations, seconds)
}

// TestMetricsLifecycleServiceObservesCreateOnce 验证成功和拒绝均只记录一个非负 duration。
func TestMetricsLifecycleServiceObservesCreateOnce(t *testing.T) {
	for _, testCase := range []struct {
		name, want string
		fake       lifecycleLoggingFake
	}{
		{"accepted", "accepted", lifecycleLoggingFake{}}, {"rejected", "rejected", lifecycleLoggingFake{createErr: domain.ErrIdempotencyConflict}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			metrics := &lifecycleMetricsFake{}
			service, _ := NewMetricsLifecycleService(&testCase.fake, metrics)
			_, _ = service.CreateAccepted(context.Background(), CreateSandbox{Image: "image"})
			if len(metrics.results) != 1 || metrics.results[0] != testCase.want || len(metrics.durations) != 1 || metrics.durations[0] < 0 {
				t.Fatalf("metrics: %#v %#v", metrics.results, metrics.durations)
			}
		})
	}
}
