package reconcile

import (
	"context"
	"time"
)

type Scheduler struct {
	interval time.Duration
	run      func(context.Context)
}

func NewScheduler(interval time.Duration, run func(context.Context)) *Scheduler {
	return &Scheduler{interval: interval, run: run}
}

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
