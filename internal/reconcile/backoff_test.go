package reconcile

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type sequenceRandom struct {
	values []int64
	index  int
}

func (r *sequenceRandom) Int64N(upper int64) (int64, error) {
	if r.index >= len(r.values) {
		return 0, errors.New("random sequence exhausted")
	}
	value := r.values[r.index]
	r.index++
	if value == -1 {
		return upper - 1, nil
	}
	return value, nil
}

// TestFullJitterBackoffCapsAndAvoidsOverflow 验证 attempt 0/1/大值和饱和乘法。
func TestFullJitterBackoffCapsAndAvoidsOverflow(t *testing.T) {
	minimum, maximum := 3*time.Second, 10*time.Second
	for _, test := range []struct {
		attempt uint32
		want    time.Duration
	}{
		{0, 3 * time.Second},
		{1, 6 * time.Second},
		{2, 10 * time.Second},
		{1_000_000, 10 * time.Second},
	} {
		delay, err := FullJitterBackoff(test.attempt, minimum, maximum, &sequenceRandom{values: []int64{-1}})
		if err != nil || delay != test.want {
			t.Fatalf("attempt=%d delay=%s want=%s err=%v", test.attempt, delay, test.want, err)
		}
	}
	nearLimit := time.Duration(1<<62 + 7)
	delay, err := FullJitterBackoff(63, nearLimit, time.Duration(1<<63-1), &sequenceRandom{values: []int64{-1}})
	if err != nil || delay != time.Duration(1<<63-1) {
		t.Fatalf("overflow case: delay=%s err=%v", delay, err)
	}
}

// TestFullJitterBackoffRandomBoundaries 验证随机区间严格为 (0, cap] 且 min=max 合法。
func TestFullJitterBackoffRandomBoundaries(t *testing.T) {
	minimum := 10 * time.Nanosecond
	low, err := FullJitterBackoff(0, minimum, minimum, &sequenceRandom{values: []int64{0}})
	if err != nil || low != time.Nanosecond {
		t.Fatalf("lower boundary: %s/%v", low, err)
	}
	high, err := FullJitterBackoff(0, minimum, minimum, &sequenceRandom{values: []int64{-1}})
	if err != nil || high != minimum {
		t.Fatalf("upper boundary: %s/%v", high, err)
	}
}

// TestFullJitterBackoffGoldenSequence 验证可控随机源产生确定序列。
func TestFullJitterBackoffGoldenSequence(t *testing.T) {
	random := &sequenceRandom{values: []int64{0, 2, 6, 14}}
	var got []time.Duration
	for attempt := uint32(0); attempt < 4; attempt++ {
		delay, err := FullJitterBackoff(attempt, 2*time.Nanosecond, 16*time.Nanosecond, random)
		if err != nil {
			t.Fatalf("attempt=%d: %v", attempt, err)
		}
		got = append(got, delay)
	}
	want := []time.Duration{1, 3, 7, 15}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence=%v, want %v", got, want)
	}
}

// TestFullJitterBackoffRejectsInvalidInputs 验证错误配置和随机源不能制造非法 delay。
func TestFullJitterBackoffRejectsInvalidInputs(t *testing.T) {
	for _, input := range []struct {
		minimum time.Duration
		maximum time.Duration
		random  Random
	}{
		{0, time.Second, &sequenceRandom{values: []int64{0}}},
		{time.Second, time.Millisecond, &sequenceRandom{values: []int64{0}}},
		{time.Second, time.Second, nil},
		{time.Second, time.Second, &sequenceRandom{values: []int64{int64(time.Second)}}},
	} {
		if delay, err := FullJitterBackoff(0, input.minimum, input.maximum, input.random); err == nil || delay != 0 {
			t.Fatalf("input=%#v delay=%s err=%v", input, delay, err)
		}
	}
}
