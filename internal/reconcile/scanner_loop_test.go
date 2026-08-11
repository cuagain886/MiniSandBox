package reconcile

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scannerFunc func(context.Context, time.Time) (CandidateScanResult, error)

func (f scannerFunc) ScanOnce(ctx context.Context, now time.Time) (CandidateScanResult, error) {
	return f(ctx, now)
}

type fixedRandom struct {
	value int64
	err   error
}

func (r fixedRandom) Int64N(upper int64) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.value < 0 || r.value >= upper {
		return 0, errors.New("fixed random out of range")
	}
	return r.value, nil
}

// TestScannerLoopScansImmediatelyAndUsesJitterBoundary 验证首次立即扫及对称 jitter 边界。
func TestScannerLoopScansImmediatelyAndUsesJitterBoundary(t *testing.T) {
	start := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	clock := newManualClock(start)
	calls := make(chan time.Time, 2)
	loop, _ := NewScannerLoop(scannerFunc(func(_ context.Context, now time.Time) (CandidateScanResult, error) {
		calls <- now
		return CandidateScanResult{}, nil
	}), clock, fixedRandom{value: 0}, time.Minute, 10*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	if got := <-calls; !got.Equal(start) {
		t.Fatalf("initial scan time: %s", got)
	}
	<-clock.timerCreated
	clock.mu.Lock()
	if len(clock.timers) != 1 || !clock.timers[0].due.Equal(start.Add(50*time.Second)) {
		clock.mu.Unlock()
		t.Fatal("minimum jitter delay was not scheduled")
	}
	clock.mu.Unlock()
	clock.Advance(50 * time.Second)
	if got := <-calls; !got.Equal(start.Add(50 * time.Second)) {
		t.Fatalf("second scan time: %s", got)
	}
	cancel()
	<-done
}

// TestScannerLoopJitterBounds 验证随机区间两端都落在配置的对称边界内。
func TestScannerLoopJitterBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int64
		want  time.Duration
	}{
		{name: "minimum", value: 0, want: 90 * time.Nanosecond},
		{name: "maximum", value: 20, want: 110 * time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			loop := &ScannerLoop{interval: 100 * time.Nanosecond, jitter: 10 * time.Nanosecond, random: fixedRandom{value: test.value}}
			if got := loop.nextDelay(); got != test.want {
				t.Fatalf("delay=%s, want %s", got, test.want)
			}
		})
	}
}

// TestScannerLoopDoesNotOverlapLongSweep 验证 timer 只在长 sweep 返回后创建。
func TestScannerLoopDoesNotOverlapLongSweep(t *testing.T) {
	clock := newManualClock(time.Unix(0, 0).UTC())
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	var active, maximum atomic.Int32
	loop, _ := NewScannerLoop(scannerFunc(func(context.Context, time.Time) (CandidateScanResult, error) {
		current := active.Add(1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return CandidateScanResult{}, nil
	}), clock, fixedRandom{}, time.Minute, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	<-started
	clock.Advance(time.Hour)
	clock.mu.Lock()
	timerCount := len(clock.timers)
	clock.mu.Unlock()
	if timerCount != 0 {
		t.Fatal("timer was scheduled during active sweep")
	}
	release <- struct{}{}
	<-clock.timerCreated
	cancel()
	release <- struct{}{}
	<-done
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent sweeps: %d", maximum.Load())
	}
}

// TestScannerLoopRecoversPanicAndStops 验证 panic/普通错误不终止后续轮次且 shutdown 无泄漏。
func TestScannerLoopRecoversPanicAndStops(t *testing.T) {
	clock := newManualClock(time.Unix(0, 0).UTC())
	var mu sync.Mutex
	calls := 0
	reported := make(chan error, 2)
	loop, _ := NewScannerLoop(scannerFunc(func(context.Context, time.Time) (CandidateScanResult, error) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		if current == 1 {
			panic("secret panic")
		}
		return CandidateScanResult{}, errors.New("scan failed")
	}), clock, fixedRandom{}, time.Minute, 0, func(err error) { reported <- err })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	first := <-reported
	if first.Error() != "candidate scanner panicked" {
		t.Fatalf("panic report: %v", first)
	}
	<-clock.timerCreated
	clock.Advance(time.Minute)
	if second := <-reported; second.Error() != "scan failed" {
		t.Fatalf("scan report: %v", second)
	}
	cancel()
	<-done
}
