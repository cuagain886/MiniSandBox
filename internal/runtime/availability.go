package runtime

import (
	"context"
	"sync"
)

// OperationAvailability 控制全局 runtime 操作是否允许开始。
//
// Docker 长时间不可用时 WaitAvailable 阻塞新 Ensure/Delete；恢复后广播唤醒。
// 等待取消不代表 sandbox 失败，reconciler 不得增加 retry attempt。
type OperationAvailability interface {
	WaitAvailable(context.Context) error
	SetAvailable(bool)
}

// AvailabilityGate 是并发安全、可重复开关的全局操作门禁。
type AvailabilityGate struct {
	mu        sync.Mutex
	available bool
	changed   chan struct{}
}

// NewAvailabilityGate 创建指定初始状态的全局门禁。
func NewAvailabilityGate(available bool) *AvailabilityGate {
	return &AvailabilityGate{available: available, changed: make(chan struct{})}
}

// WaitAvailable 等待门禁开放或调用 context 取消。
func (g *AvailabilityGate) WaitAvailable(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.available {
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SetAvailable 更新状态并广播给全部等待者；相同状态是幂等 no-op。
func (g *AvailabilityGate) SetAvailable(available bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.available == available {
		return
	}
	g.available = available
	close(g.changed)
	g.changed = make(chan struct{})
}
