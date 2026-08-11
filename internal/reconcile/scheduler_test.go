package reconcile

import (
	"context"
	"testing"
	"time"
)

// TestSchedulerUsesInjectedClock 验证 scheduler 的触发和关闭不依赖 wall clock sleep。
func TestSchedulerUsesInjectedClock(t *testing.T) {
	clock := newManualClock(time.Unix(0, 0).UTC())
	calls := make(chan struct{}, 2)
	scheduler := NewSchedulerWithClock(time.Minute, func(context.Context) { calls <- struct{}{} }, clock)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); scheduler.Run(ctx) }()
	<-clock.tickerCreated
	clock.Advance(2 * time.Minute)
	for index := 0; index < 2; index++ {
		<-calls
	}
	cancel()
	<-done
}
