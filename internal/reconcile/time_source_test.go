package reconcile

import (
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	mu            sync.Mutex
	now           time.Time
	timers        []*manualTimer
	tickers       []*manualTicker
	tickerCreated chan struct{}
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now, tickerCreated: make(chan struct{}, 16)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(duration time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, channel: make(chan time.Time, 64), due: c.now.Add(duration), active: true}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *manualClock) NewTicker(interval time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	ticker := &manualTicker{clock: c, channel: make(chan time.Time, 64), interval: interval, next: c.now.Add(interval), active: true}
	c.tickers = append(c.tickers, ticker)
	c.tickerCreated <- struct{}{}
	return ticker
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
	for _, timer := range c.timers {
		if timer.active && !timer.due.After(c.now) {
			timer.active = false
			timer.channel <- timer.due
		}
	}
	for _, ticker := range c.tickers {
		for ticker.active && !ticker.next.After(c.now) {
			ticker.channel <- ticker.next
			ticker.next = ticker.next.Add(ticker.interval)
		}
	}
}

type manualTimer struct {
	clock   *manualClock
	channel chan time.Time
	due     time.Time
	active  bool
}

func (t *manualTimer) C() <-chan time.Time { return t.channel }
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := t.active
	t.active = false
	return active
}
func (t *manualTimer) Reset(duration time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := t.active
	t.active = true
	t.due = t.clock.now.Add(duration)
	return active
}

type manualTicker struct {
	clock    *manualClock
	channel  chan time.Time
	interval time.Duration
	next     time.Time
	active   bool
}

func (t *manualTicker) C() <-chan time.Time { return t.channel }
func (t *manualTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.active = false
}

// TestManualClockAdvanceStopReset 验证 timer 可控推进、停止和重置。
func TestManualClockAdvanceStopReset(t *testing.T) {
	start := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	clock := newManualClock(start)
	timer := clock.NewTimer(time.Minute)
	clock.Advance(30 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired early")
	default:
	}
	if !timer.Stop() || timer.Stop() {
		t.Fatal("timer stop state is incorrect")
	}
	if timer.Reset(10 * time.Second) {
		t.Fatal("reset reported inactive timer as active")
	}
	clock.Advance(10 * time.Second)
	if fired := <-timer.C(); !fired.Equal(start.Add(40 * time.Second)) {
		t.Fatalf("timer fired at %s", fired)
	}
}

// TestManualClockConcurrentWaitersAndRepeatedTicker 验证并发等待者和重复 firing 无需 sleep。
func TestManualClockConcurrentWaitersAndRepeatedTicker(t *testing.T) {
	clock := newManualClock(time.Unix(0, 0).UTC())
	ticker := clock.NewTicker(time.Second)
	const waiters = 8
	var wait sync.WaitGroup
	wait.Add(waiters)
	for index := 0; index < waiters; index++ {
		go func() {
			defer wait.Done()
			<-ticker.C()
		}()
	}
	clock.Advance(waiters * time.Second)
	wait.Wait()
	ticker.Stop()
	clock.Advance(time.Hour)
	select {
	case <-ticker.C():
		t.Fatal("stopped ticker produced another event")
	default:
	}
}

// TestCryptoRandomBounds 验证生产随机源遵守半开区间和非法边界。
func TestCryptoRandomBounds(t *testing.T) {
	random := CryptoRandom{}
	if _, err := random.Int64N(0); err == nil {
		t.Fatal("zero upper bound was accepted")
	}
	for index := 0; index < 32; index++ {
		value, err := random.Int64N(7)
		if err != nil || value < 0 || value >= 7 {
			t.Fatalf("random value=%d err=%v", value, err)
		}
	}
}

var _ Clock = (*manualClock)(nil)
