package reconcile

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
)

// RunnerProbeMetrics 只接受 healthy、unhealthy 或 error 固定分类。
type RunnerProbeMetrics interface{ ObserveRunnerProbe(result string) }

// MetricsRunnerProbe 在真实 probe 返回后记录一次分类，同时保留可选 network probe 能力。
type MetricsRunnerProbe struct {
	next    RunnerProbe
	metrics RunnerProbeMetrics
}

// NewMetricsRunnerProbe 创建 runner probe 指标装饰器。
func NewMetricsRunnerProbe(next RunnerProbe, metrics RunnerProbeMetrics) (*MetricsRunnerProbe, error) {
	if next == nil || metrics == nil {
		return nil, fmt.Errorf("runner probe metrics dependencies: %w", domain.ErrInvalid)
	}
	return &MetricsRunnerProbe{next: next, metrics: metrics}, nil
}

// Probe 调用真实健康探测并在完成后分类。
func (p *MetricsRunnerProbe) Probe(ctx context.Context, sandboxID string, version int) error {
	err := p.next.Probe(ctx, sandboxID, version)
	p.metrics.ObserveRunnerProbe(probeMetricResult(err))
	return err
}

// ProbeNetwork 仅当底层实现 network probe 时发起真实探测，否则返回固定错误并且不计数。
func (p *MetricsRunnerProbe) ProbeNetwork(ctx context.Context, sandboxID string, version int) (string, error) {
	next, ok := p.next.(RunnerNetworkProbe)
	if !ok {
		return "", errors.New("runner network probe is unavailable")
	}
	identity, err := next.ProbeNetwork(ctx, sandboxID, version)
	p.metrics.ObserveRunnerProbe(probeMetricResult(err))
	return identity, err
}

func probeMetricResult(err error) string {
	if err == nil {
		return "healthy"
	}
	if errors.Is(err, domain.ErrRunnerUnhealthy) {
		return "unhealthy"
	}
	return "error"
}
