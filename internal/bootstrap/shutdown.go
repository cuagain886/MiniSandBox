package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/reconcile"
)

// shutdownCoordinator 在一个总 grace 内按安全依赖顺序停止可靠性组件。
type shutdownCoordinator struct {
	grace       time.Duration
	readiness   *controlapi.Readiness
	admission   interface{ Close(context.Context) error }
	maintenance interface{ Close(context.Context) error }
	queue       *reconcile.WakeQueue
	worker      interface{ Close(context.Context) error }
	runtime     io.Closer
	store       io.Closer

	once sync.Once
	err  error
}

// Close 先撤销准入和 Wake 来源，再等待 worker，最后关闭其依赖。
// 重复调用返回首次关闭结果，不重复触发任何组件副作用。
func (s *shutdownCoordinator) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() { s.err = s.closeOnce() })
	return s.err
}

func (s *shutdownCoordinator) closeOnce() error {
	markNotReady(s.readiness)
	ctx, cancel := context.WithTimeout(context.Background(), s.grace)
	defer cancel()
	var result error
	result = errors.Join(result, closeContextStep(ctx, "sandbox HTTP server", s.admission))
	result = errors.Join(result, closeContextStep(ctx, "reliability maintenance", s.maintenance))
	if s.queue != nil {
		s.queue.Close()
	}
	result = errors.Join(result, closeContextStep(ctx, "reconcile worker", s.worker))
	result = errors.Join(result, closeIOResource("sandbox runtime", s.runtime))
	result = errors.Join(result, closeIOResource("sandbox store", s.store))
	return result
}

func closeContextStep(ctx context.Context, name string, resource interface{ Close(context.Context) error }) error {
	if resource == nil {
		return nil
	}
	if err := resource.Close(ctx); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

func closeIOResource(name string, resource io.Closer) error {
	if resource == nil {
		return nil
	}
	if err := resource.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}
