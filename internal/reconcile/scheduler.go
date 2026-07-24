package reconcile

import (
	"context"
	"time"
)

// Scheduler 按固定间隔触发全局状态扫描。
type Scheduler struct {
	interval time.Duration
	run      func(context.Context)
}

// NewScheduler 创建周期调度器；run 应快速响应传入 context 的取消信号。
func NewScheduler(interval time.Duration, run func(context.Context)) *Scheduler {
	return &Scheduler{interval: interval, run: run}
}

// Run 阻塞运行调度循环，直到 context 被取消。
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.run(ctx)
		}
	}
}
