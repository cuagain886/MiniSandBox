package reconcile

import (
	"context"
)

// TTLDueCallback 接收 scheduler 已从 heap 弹出的候选到期 entry。
// callback 必须重读 Store 验证租约；entry 本身不授权任何状态写入。
type TTLDueCallback func(context.Context, TTLHeapEntry)

// TTLScheduler 用一个可复用 timer 驱动全部 sandbox 的 TTL 候选事件。
type TTLScheduler struct {
	heap     *TTLHeap
	clock    Clock
	callback TTLDueCallback
	wake     chan struct{}
}

// NewTTLScheduler 创建共享 heap 和注入时钟的 TTL 调度器。
func NewTTLScheduler(clock Clock, callback TTLDueCallback) *TTLScheduler {
	return &TTLScheduler{
		heap: NewTTLHeap(), clock: clock, callback: callback, wake: make(chan struct{}, 1),
	}
}

// Upsert 保存最新租约并在排序实际变化时唤醒 timer loop。
func (s *TTLScheduler) Upsert(entry TTLHeapEntry) bool {
	changed := s.heap.Upsert(entry)
	if changed {
		s.notify()
	}
	return changed
}

// Remove 幂等移除租约，并在实际删除时唤醒 timer loop。
func (s *TTLScheduler) Remove(sandboxID string) bool {
	changed := s.heap.Remove(sandboxID)
	if changed {
		s.notify()
	}
	return changed
}

// Peek 返回当前最早租约，主要供启动恢复和测试观察，不驱动到期决策。
func (s *TTLScheduler) Peek() (TTLHeapEntry, bool) {
	return s.heap.Peek()
}

// Len 返回 scheduler 当前保存的唯一 sandbox 数量。
func (s *TTLScheduler) Len() int {
	return s.heap.Len()
}

// Run 阻塞运行单 timer 循环，直到 context 取消。
//
// timer firing 只把到期 entry 交给 callback；Store 复核和 expire intent 属于
// 后续层。callback 在 heap mutex 外串行执行，慢 callback 不阻塞并发 upsert。
func (s *TTLScheduler) Run(ctx context.Context) {
	if s.clock == nil || s.heap == nil {
		return
	}
	var timer Timer
	defer func() { stopAndDrainTTLTimer(timer) }()
	for {
		entry, ok := s.heap.Peek()
		if !ok {
			stopAndDrainTTLTimer(timer)
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}

		now := s.clock.Now().UTC()
		delay := entry.ExpectedExpiresAt.Sub(now)
		if delay <= 0 {
			for _, due := range s.heap.popDue(now) {
				if s.callback != nil {
					s.callback(ctx, due)
				}
				if ctx.Err() != nil {
					return
				}
			}
			continue
		}

		if timer == nil {
			timer = s.clock.NewTimer(delay)
		} else {
			stopAndDrainTTLTimer(timer)
			timer.Reset(delay)
		}
		// fake clock 或 wall clock 可能在计算 delay 后、真正 arm timer 前推进。
		// arm 后再次比较绝对 expiry，避免按已过时的相对 delay 延后到期事件。
		if !s.clock.Now().UTC().Before(entry.ExpectedExpiresAt) {
			stopAndDrainTTLTimer(timer)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			stopAndDrainTTLTimer(timer)
		case <-timer.C():
		}
	}
}

func (s *TTLScheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func stopAndDrainTTLTimer(timer Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
}

var _ interface {
	Upsert(TTLHeapEntry) bool
	Remove(string) bool
} = (*TTLScheduler)(nil)
