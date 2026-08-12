package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"minisandbox/internal/domain"
)

// LifecycleMetrics 是 create 完成点依赖的计数与耗时端口。
type LifecycleMetrics interface {
	// ObserveCreate 记录 accepted、rejected 或 error。
	ObserveCreate(result string)
	// ObserveCreateDuration 记录使用 Go 单调时钟计算的秒数。
	ObserveCreateDuration(seconds float64)
}

// MetricsLifecycleService 在 create 唯一完成点更新指标，其余生命周期调用透明转发。
type MetricsLifecycleService struct {
	next    LifecycleOperations
	metrics LifecycleMetrics
}

// NewMetricsLifecycleService 创建 lifecycle metrics 装饰器。
func NewMetricsLifecycleService(next LifecycleOperations, metrics LifecycleMetrics) (*MetricsLifecycleService, error) {
	if next == nil || metrics == nil {
		return nil, fmt.Errorf("lifecycle metrics dependencies: %w", domain.ErrInvalid)
	}
	return &MetricsLifecycleService{next: next, metrics: metrics}, nil
}

// CreateAccepted 使用含单调分量的 time.Now 计算控制面请求耗时并只观察一次。
func (s *MetricsLifecycleService) CreateAccepted(ctx context.Context, command CreateSandbox) (IdempotentCreateOutcome, error) {
	started := time.Now()
	outcome, err := s.next.CreateAccepted(ctx, command)
	s.metrics.ObserveCreate(createMetricResult(err))
	s.metrics.ObserveCreateDuration(time.Since(started).Seconds())
	return outcome, err
}

// Get 透明转发读取。
func (s *MetricsLifecycleService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	return s.next.Get(ctx, id)
}

// Delete 透明转发删除意图。
func (s *MetricsLifecycleService) Delete(ctx context.Context, command DeleteSandbox) (domain.Sandbox, error) {
	return s.next.Delete(ctx, command)
}

// Renew 透明转发续租。
func (s *MetricsLifecycleService) Renew(ctx context.Context, command RenewSandbox) (domain.Sandbox, error) {
	return s.next.Renew(ctx, command)
}

func createMetricResult(err error) string {
	if err == nil {
		return "accepted"
	}
	for _, rejected := range []error{domain.ErrInvalid, domain.ErrInvalidTTL, domain.ErrInvalidExpiration,
		domain.ErrIdempotencyConflict, domain.ErrSandboxLimitReached, domain.ErrOutboundNotAllowed} {
		if errors.Is(err, rejected) {
			return "rejected"
		}
	}
	return "error"
}
