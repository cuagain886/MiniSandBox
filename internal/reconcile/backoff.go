package reconcile

import (
	"errors"
	"time"
)

const maxBackoffAttempt uint32 = 63

// FullJitterBackoff 计算 capped exponential full-jitter delay。
//
// attempt=0 的 cap 为 minimum，之后每次饱和翻倍到 maximum；成功结果严格位于
// (0, cap]，因此不会形成零延迟 busy-loop。
func FullJitterBackoff(attempt uint32, minimum, maximum time.Duration, random Random) (time.Duration, error) {
	if minimum <= 0 || maximum < minimum || random == nil {
		return 0, errors.New("invalid retry backoff configuration")
	}
	if attempt > maxBackoffAttempt {
		attempt = maxBackoffAttempt
	}
	cap := minimum
	for step := uint32(0); step < attempt && cap < maximum; step++ {
		if cap > maximum-cap {
			cap = maximum
			break
		}
		cap *= 2
	}
	if cap > maximum {
		cap = maximum
	}
	value, err := random.Int64N(int64(cap))
	if err != nil {
		return 0, err
	}
	if value < 0 || value >= int64(cap) {
		return 0, errors.New("retry random source returned an out-of-range value")
	}
	return time.Duration(value + 1), nil
}
