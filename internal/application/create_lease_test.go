package application

import (
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// TestResolveCreateLeaseBoundaries 验证默认值及允许区间边界都规范化为整秒租期。
func TestResolveCreateLeaseBoundaries(t *testing.T) {
	now := time.Date(2028, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC+8", 8*60*60))
	tests := []struct {
		name    string
		seconds *int64
		want    time.Duration
		present bool
	}{
		{name: "default", want: 30 * time.Minute},
		{name: "minimum", seconds: leaseSeconds(60), want: time.Minute, present: true},
		{name: "maximum", seconds: leaseSeconds(86400), want: 24 * time.Hour, present: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &recordingClock{now: now}
			service := &SandboxService{clock: clock, createPolicy: CreatePolicy{
				DefaultTTL: 30 * time.Minute, MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour,
			}}
			got, err := service.resolveCreateLease(tt.seconds)
			if err != nil {
				t.Fatalf("resolve lease: %v", err)
			}
			if got.TTL != tt.want || !got.Now.Equal(now) || got.Now.Location() != time.UTC ||
				!got.ExpiresAt.Equal(now.Add(tt.want)) || got.ExpiresAt.Location() != time.UTC {
				t.Fatalf("resolved lease: %#v", got)
			}
			if (got.CanonicalTTLSeconds != nil) != tt.present {
				t.Fatalf("canonical TTL presence: got %v, want %v", got.CanonicalTTLSeconds, tt.present)
			}
			if tt.present && *got.CanonicalTTLSeconds != *tt.seconds {
				t.Fatalf("canonical TTL: got %d, want %d", *got.CanonicalTTLSeconds, *tt.seconds)
			}
			if clock.calls != 1 {
				t.Fatalf("clock calls: got %d, want 1", clock.calls)
			}
		})
	}
}

// TestResolveCreateLeaseRejectsInvalidAndOverflow 验证非法 duration 不读时钟，时间越界安全失败。
func TestResolveCreateLeaseRejectsInvalidAndOverflow(t *testing.T) {
	tests := []struct {
		name    string
		seconds int64
	}{
		{name: "zero", seconds: 0},
		{name: "negative", seconds: -1},
		{name: "below minimum", seconds: 59},
		{name: "above maximum", seconds: 86401},
		{name: "duration overflow", seconds: int64((time.Duration(1<<63-1))/time.Second) + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &recordingClock{now: time.Now()}
			service := &SandboxService{clock: clock, createPolicy: CreatePolicy{
				DefaultTTL: 30 * time.Minute, MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour,
			}}
			_, err := service.resolveCreateLease(&tt.seconds)
			if !errors.Is(err, domain.ErrInvalidTTL) {
				t.Fatalf("resolve invalid lease: got %v, want ErrInvalidTTL", err)
			}
			if clock.calls != 0 {
				t.Fatalf("invalid duration read clock %d times", clock.calls)
			}
		})
	}

	clock := &recordingClock{now: time.Date(9999, 12, 31, 23, 59, 30, 0, time.UTC)}
	service := &SandboxService{clock: clock, createPolicy: CreatePolicy{
		DefaultTTL: time.Minute, MinimumTTL: time.Minute, MaximumTTL: time.Minute,
	}}
	_, err := service.resolveCreateLease(nil)
	if !errors.Is(err, domain.ErrInvalidTTL) || clock.calls != 1 {
		t.Fatalf("time overflow: err=%v clock calls=%d", err, clock.calls)
	}
}

func leaseSeconds(value int64) *int64 {
	return &value
}
