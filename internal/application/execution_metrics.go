package application

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// ExecutionMetrics 是 execution 用例依赖的低基数计数端口，不接收 sandbox、execution 或用户内容。
type ExecutionMetrics interface {
	// ObserveExecutionRequest 按固定 mode/result 记录一次控制面请求结果。
	ObserveExecutionRequest(mode, result string)
	// ObserveForegroundTerminal 记录前台流首次观察到的合法 terminal 分类。
	ObserveForegroundTerminal(result string)
}

// MetricsExecutionService 为 Execute 添加控制面可证明的计数，不改变其他 execution 用例语义。
type MetricsExecutionService struct {
	next    *ExecutionService
	metrics ExecutionMetrics
}

// NewMetricsExecutionService 创建 execution metrics 装饰器。
func NewMetricsExecutionService(next *ExecutionService, metrics ExecutionMetrics) (*MetricsExecutionService, error) {
	if next == nil || metrics == nil {
		return nil, fmt.Errorf("execution metrics dependencies: %w", domain.ErrInvalid)
	}
	return &MetricsExecutionService{next: next, metrics: metrics}, nil
}

// Execute 记录请求结果，并仅包装成功的前台流以观察唯一合法终态。
func (s *MetricsExecutionService) Execute(ctx context.Context, command Execute) (ExecutionResult, error) {
	mode := "foreground"
	if command.Background {
		mode = "background"
	}
	result, err := s.next.Execute(ctx, command)
	s.metrics.ObserveExecutionRequest(mode, executionRequestResult(err))
	if err == nil && !command.Background && result.Stream != nil {
		result.Stream = &terminalObservingStream{next: result.Stream, metrics: s.metrics}
	}
	return result, err
}

// Status 原样转发查询，不计入 execution request counter。
func (s *MetricsExecutionService) Status(ctx context.Context, sandboxID, executionID string) (ExecutionStatus, error) {
	return s.next.Status(ctx, sandboxID, executionID)
}

// Cancel 原样转发取消，不计入 execution request counter。
func (s *MetricsExecutionService) Cancel(ctx context.Context, sandboxID, executionID string) (CancelDisposition, error) {
	return s.next.Cancel(ctx, sandboxID, executionID)
}

// Logs 原样转发日志读取，不计入 execution request counter。
func (s *MetricsExecutionService) Logs(ctx context.Context, sandboxID, executionID string, cursor uint64, limit int) (ExecutionLogPage, error) {
	return s.next.Logs(ctx, sandboxID, executionID, cursor, limit)
}

type terminalObservingStream struct {
	next    ExecutionEventStream
	metrics ExecutionMetrics
	once    sync.Once
}

func (s *terminalObservingStream) Consume(consume func(protocol.ExecutionEvent) error) error {
	return s.next.Consume(func(event protocol.ExecutionEvent) error {
		if event.Validate() == nil {
			if result, ok := foregroundTerminalResult(event.Type); ok {
				s.once.Do(func() { s.metrics.ObserveForegroundTerminal(result) })
			}
		}
		return consume(event)
	})
}

func (s *terminalObservingStream) Close() error { return s.next.Close() }

func executionRequestResult(err error) string {
	if err == nil {
		return "accepted"
	}
	for _, rejected := range []error{domain.ErrInvalidExecutionRequest, domain.ErrSandboxNotRunning,
		domain.ErrExecutionLimitReached, domain.ErrShellNotFound, domain.ErrInvalidCWD, domain.ErrOutboundNotAllowed} {
		if errors.Is(err, rejected) {
			return "rejected"
		}
	}
	return "error"
}

func foregroundTerminalResult(eventType protocol.EventType) (string, bool) {
	switch eventType {
	case protocol.EventExited:
		return "exited", true
	case protocol.EventFailed:
		return "failed", true
	case protocol.EventCancelled:
		return "cancelled", true
	case protocol.EventTimedOut:
		return "timed_out", true
	default:
		return "", false
	}
}
