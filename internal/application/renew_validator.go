package application

import (
	"time"

	"minisandbox/internal/domain"
)

// renewValidation 保存一次服务端时钟边界上的规范化续期决策。
type renewValidation struct {
	RequestedExpiresAt time.Time
	Now                time.Time
	NoOp               bool
}

// validateRenew 校验当前 Store snapshot 上只能延长的绝对租约语义。
func (s *SandboxService) validateRenew(current domain.Sandbox, requested time.Time) (renewValidation, error) {
	if s.clock == nil || s.createPolicy.MinimumTTL <= 0 ||
		s.createPolicy.MaximumTTL < s.createPolicy.MinimumTTL || requested.IsZero() {
		return renewValidation{}, domain.ErrInvalidExpiration
	}
	now := s.clock.Now().UTC()
	if current.DesiredState != domain.DesiredRunning || current.ObservedState == domain.StateTerminated ||
		current.ExpiresAt == nil || !now.Before(*current.ExpiresAt) {
		return renewValidation{}, domain.ErrSandboxExpiring
	}
	requested = requested.UTC()
	if requested.Year() < 0 || requested.Year() > 9999 {
		return renewValidation{}, domain.ErrInvalidExpiration
	}
	if requested.Equal(*current.ExpiresAt) {
		return renewValidation{RequestedExpiresAt: requested, Now: now, NoOp: true}, nil
	}
	if requested.Before(*current.ExpiresAt) {
		return renewValidation{}, domain.ErrLeaseConflict
	}
	minimum := now.Add(s.createPolicy.MinimumTTL)
	maximum := now.Add(s.createPolicy.MaximumTTL)
	if !minimum.After(now) || !maximum.After(now) || minimum.Year() > 9999 || maximum.Year() > 9999 {
		return renewValidation{}, domain.ErrInvalidExpiration
	}
	if requested.Before(minimum) || requested.After(maximum) {
		return renewValidation{}, domain.ErrInvalidExpiration
	}
	return renewValidation{RequestedExpiresAt: requested, Now: now}, nil
}
