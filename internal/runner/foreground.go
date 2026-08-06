package runner

import (
	"context"
	"errors"
	"sync"
)

// ForegroundCoordinator 把前台 execution 绑定到 runner server 与单次 HTTP 请求的生命周期，
// 但不负责 SSE framing、写超时或响应序列化。
type ForegroundCoordinator struct {
	done chan struct{}

	mu  sync.RWMutex
	err error
}

// StartForegroundCoordinator 开始监听 serverContext、requestContext 和 execution 终态。
// 请求断开使用 foreground_disconnect，server 停止使用 runner_shutdown；两者对外均映射 cancelled。
func StartForegroundCoordinator(
	serverContext context.Context,
	requestContext context.Context,
	manager *Manager,
	id ExecutionID,
) (*ForegroundCoordinator, error) {
	if serverContext == nil || requestContext == nil || manager == nil || id == "" {
		return nil, errors.New("foreground coordinator is not configured")
	}
	terminal, err := manager.foregroundCompletion(id)
	if err != nil {
		return nil, err
	}
	coordinator := &ForegroundCoordinator{done: make(chan struct{})}
	go coordinator.run(serverContext, requestContext, terminal, manager, id)
	return coordinator, nil
}

// Wait 等待前台生命周期停止监听；返回值只包含内部协调错误，不暴露 context cause 文本。
func (c *ForegroundCoordinator) Wait(ctx context.Context) error {
	if c == nil || c.done == nil || ctx == nil {
		return errors.New("foreground coordinator is unavailable")
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

func (c *ForegroundCoordinator) run(
	serverContext context.Context,
	requestContext context.Context,
	terminal <-chan struct{},
	manager *Manager,
	id ExecutionID,
) {
	defer close(c.done)
	var reason TerminationReason
	select {
	case <-terminal:
		return
	case <-serverContext.Done():
		reason = TerminationRunnerShutdown
	case <-requestContext.Done():
		reason = TerminationForegroundDisconnect
	}
	// 只传递稳定内部分类，不把 context.Cause、网络错误或请求信息带进 terminal event。
	err := manager.requestCancellation(context.Background(), id, reason)
	if err != nil {
		c.mu.Lock()
		c.err = errors.New("foreground execution cancellation failed")
		c.mu.Unlock()
	}
}
