package reconcile

import (
	cryptorand "crypto/rand"
	"errors"
	"math/big"
	"time"
)

// Timer 是可停止、可复用的单次时间事件。
type Timer interface {
	// C 返回容量由实现控制的 firing channel。
	C() <-chan time.Time
	// Stop 停止尚未触发的 timer，并返回它此前是否活跃。
	Stop() bool
	// Reset 从当前 Clock 时间重新安排 timer，并返回它此前是否活跃。
	Reset(time.Duration) bool
}

// Ticker 是按固定间隔产生时间事件的周期源。
type Ticker interface {
	// C 返回周期 firing channel。
	C() <-chan time.Time
	// Stop 停止后续 firing；允许重复调用。
	Stop()
}

// Clock 为可靠性控制循环提供当前时间和可替换的等待原语。
type Clock interface {
	// Now 返回当前时刻；持久化边界仍须归一化为 UTC。
	Now() time.Time
	// NewTimer 创建一次性 timer，duration 必须为正数。
	NewTimer(time.Duration) Timer
	// NewTicker 创建周期 ticker，interval 必须为正数。
	NewTicker(time.Duration) Ticker
}

// Random 为 jitter 和退避提供无全局状态的有界随机源。
type Random interface {
	// Int64N 返回 [0, upper) 的均匀值；upper 必须为正数。
	Int64N(upper int64) (int64, error)
}

// SystemClock 使用标准库 wall clock、timer 和 ticker。
type SystemClock struct{}

// Now 返回当前时间。
func (SystemClock) Now() time.Time { return time.Now() }

// NewTimer 创建标准库 timer。
func (SystemClock) NewTimer(duration time.Duration) Timer {
	return systemTimer{timer: time.NewTimer(duration)}
}

// NewTicker 创建标准库 ticker。
func (SystemClock) NewTicker(interval time.Duration) Ticker {
	return systemTicker{ticker: time.NewTicker(interval)}
}

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time               { return t.timer.C }
func (t systemTimer) Stop() bool                        { return t.timer.Stop() }
func (t systemTimer) Reset(duration time.Duration) bool { return t.timer.Reset(duration) }

type systemTicker struct{ ticker *time.Ticker }

func (t systemTicker) C() <-chan time.Time { return t.ticker.C }
func (t systemTicker) Stop()               { t.ticker.Stop() }

// CryptoRandom 使用 crypto/rand 提供生产 jitter 随机值。
type CryptoRandom struct{}

// Int64N 返回无模偏差的有界随机值。
func (CryptoRandom) Int64N(upper int64) (int64, error) {
	if upper <= 0 {
		return 0, errors.New("random upper bound must be positive")
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(upper))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}

var _ Clock = SystemClock{}
var _ Random = CryptoRandom{}
