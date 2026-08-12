package reconcile

import (
	"context"
	"errors"
	"testing"

	"minisandbox/internal/domain"
)

type probeMetricsFake struct{ results []string }

func (m *probeMetricsFake) ObserveRunnerProbe(result string) { m.results = append(m.results, result) }

type probeMetricsProbe struct {
	err      error
	identity string
}

func (p *probeMetricsProbe) Probe(context.Context, string, int) error { return p.err }
func (p *probeMetricsProbe) ProbeNetwork(context.Context, string, int) (string, error) {
	return p.identity, p.err
}

// TestMetricsRunnerProbeClassifiesActualProbeResults 验证普通与 network probe 的三种固定结果。
func TestMetricsRunnerProbeClassifiesActualProbeResults(t *testing.T) {
	metrics := &probeMetricsFake{}
	probe := &probeMetricsProbe{identity: "linux-netns:1:2"}
	decorator, _ := NewMetricsRunnerProbe(probe, metrics)
	_ = decorator.Probe(context.Background(), "sandbox-1", 1)
	probe.err = domain.ErrRunnerUnhealthy
	_, _ = decorator.ProbeNetwork(context.Background(), "sandbox-1", 1)
	probe.err = errors.New("transport failed")
	_ = decorator.Probe(context.Background(), "sandbox-1", 1)
	want := []string{"healthy", "unhealthy", "error"}
	if len(metrics.results) != len(want) {
		t.Fatalf("results: %#v", metrics.results)
	}
	for index := range want {
		if metrics.results[index] != want[index] {
			t.Fatalf("result %d: %q", index, metrics.results[index])
		}
	}
}
