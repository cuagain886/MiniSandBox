package application

import (
	"errors"
	"testing"
	"time"

	"minisandbox/internal/domain"
)

// TestValidateRenewSemantics 验证幂等、缩短、边界、过期和删除优先级。
func TestValidateRenewSemantics(t *testing.T) {
	now := time.Date(2028, 6, 7, 8, 9, 10, 0, time.UTC)
	currentExpiry := now.Add(30 * time.Minute)
	base := domain.Sandbox{
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning, ExpiresAt: &currentExpiry,
	}
	tests := []struct {
		name      string
		mutate    func(*domain.Sandbox)
		requested time.Time
		wantErr   error
		wantNoOp  bool
	}{
		{name: "equal no-op", requested: currentExpiry, wantNoOp: true},
		{name: "shorten", requested: currentExpiry.Add(-time.Second), wantErr: domain.ErrLeaseConflict},
		{name: "minimum boundary", requested: now.Add(time.Hour)},
		{name: "maximum boundary", requested: now.Add(24 * time.Hour)},
		{name: "below minimum", requested: now.Add(time.Hour - time.Second), wantErr: domain.ErrInvalidExpiration},
		{name: "above maximum", requested: now.Add(24*time.Hour + time.Second), wantErr: domain.ErrInvalidExpiration},
		{name: "already expired", requested: now.Add(2 * time.Hour), mutate: func(s *domain.Sandbox) {
			expired := now
			s.ExpiresAt = &expired
		}, wantErr: domain.ErrSandboxExpiring},
		{name: "terminating", requested: now.Add(2 * time.Hour), mutate: func(s *domain.Sandbox) {
			s.DesiredState = domain.DesiredTerminated
		}, wantErr: domain.ErrSandboxExpiring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := base
			if tt.mutate != nil {
				t.mutate(&current)
			}
			clock := &recordingClock{now: now}
			service := &SandboxService{clock: clock, createPolicy: CreatePolicy{MinimumTTL: time.Hour, MaximumTTL: 24 * time.Hour}}
			got, err := service.validateRenew(current, tt.requested)
			if !errors.Is(err, tt.wantErr) || got.NoOp != tt.wantNoOp || clock.calls != 1 {
				t.Fatalf("validation: got=%#v err=%v clock=%d", got, err, clock.calls)
			}
		})
	}
}

// TestValidateRenewNormalizesTimezone 验证接受值转 UTC 且不使用客户端时钟。
func TestValidateRenewNormalizesTimezone(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2028, 6, 7, 8, 0, 0, 0, location)
	currentExpiry := now.Add(time.Hour)
	requested := now.Add(2 * time.Hour)
	service := &SandboxService{clock: &recordingClock{now: now}, createPolicy: CreatePolicy{MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour}}
	got, err := service.validateRenew(domain.Sandbox{
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning, ExpiresAt: &currentExpiry,
	}, requested)
	if err != nil || got.RequestedExpiresAt.Location() != time.UTC || !got.RequestedExpiresAt.Equal(requested) || got.Now.Location() != time.UTC {
		t.Fatalf("timezone normalization: %#v/%v", got, err)
	}
}

// TestValidateRenewRejectsTimeOverflow 验证服务端边界无法表示为 RFC3339 时安全失败。
func TestValidateRenewRejectsTimeOverflow(t *testing.T) {
	now := time.Date(9999, 12, 31, 23, 0, 0, 0, time.UTC)
	currentExpiry := now.Add(10 * time.Minute)
	service := &SandboxService{clock: &recordingClock{now: now}, createPolicy: CreatePolicy{MinimumTTL: time.Minute, MaximumTTL: 24 * time.Hour}}
	_, err := service.validateRenew(domain.Sandbox{
		DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning, ExpiresAt: &currentExpiry,
	}, now.Add(20*time.Minute))
	if !errors.Is(err, domain.ErrInvalidExpiration) {
		t.Fatalf("overflow: got %v, want ErrInvalidExpiration", err)
	}
}
