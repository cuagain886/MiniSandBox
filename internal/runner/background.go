package runner

import (
	"context"
	"errors"
	"sync"
)

// BackgroundCoordinator 将后台 execution 仅绑定到 runner server lifetime 和 manager 终态。
// 创建请求 context 不进入该对象，因此 202 返回、响应写失败或客户端断开都不会取消用户进程。
type BackgroundCoordinator struct {
	done chan struct{}

	mu  sync.RWMutex
	err error
}

// StartBackgroundCoordinator 在 execution 已注册并绑定取消 handler 后启动后台生命周期监听。
// 配置失败时不会创建 goroutine；serverContext 结束时使用 runner_shutdown 收敛 execution。
func StartBackgroundCoordinator(
	serverContext context.Context,
	manager *Manager,
	id ExecutionID,
) (*BackgroundCoordinator, error) {
	if serverContext == nil || manager == nil || id == "" {
		return nil, errors.New("background coordinator is not configured")
	}
	terminal, err := manager.executionCompletion(id)
	if err != nil {
		return nil, err
	}
	coordinator := &BackgroundCoordinator{done: make(chan struct{})}
	go coordinator.run(serverContext, terminal, manager, id)
	return coordinator, nil
}

// Wait 等待后台生命周期监听结束；调用方 context 只限制等待，不传播给 execution。
func (c *BackgroundCoordinator) Wait(ctx context.Context) error {
	if c == nil || c.done == nil || ctx == nil {
		return errors.New("background coordinator is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.err
	}
}

func (c *BackgroundCoordinator) run(
	serverContext context.Context,
	terminal <-chan struct{},
	manager *Manager,
	id ExecutionID,
) {
	defer close(c.done)
	select {
	case <-terminal:
		return
	case <-serverContext.Done():
	}
	if err := manager.requestCancellation(context.Background(), id, TerminationRunnerShutdown); err != nil {
		c.mu.Lock()
		c.err = errors.New("background execution cancellation failed")
		c.mu.Unlock()
	}
}
