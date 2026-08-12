package application

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
	"minisandbox/internal/testcrashpoint"
)

const renewCASAttempts = 3

// Renew 以有限乐观并发循环延长租约，并始终保留竞争者写入的更晚 expiry。
func (s *SandboxService) Renew(ctx context.Context, command RenewSandbox) (domain.Sandbox, error) {
	if command.SandboxID == "" {
		return domain.Sandbox{}, domain.ErrInvalid
	}
	current, err := s.Get(ctx, command.SandboxID)
	if err != nil {
		return domain.Sandbox{}, err
	}
	for attempt := 0; attempt < renewCASAttempts; attempt++ {
		decision, err := s.validateRenew(current, command.ExpiresAt)
		if err != nil {
			return domain.Sandbox{}, err
		}
		if decision.NoOp {
			return current, nil
		}
		testcrashpoint.Hit("renew.before-store-cas")
		updated, err := s.store.Renew(ctx, storeport.RenewUpdate{
			ID: current.ID, ExpectedRevision: current.Revision,
			Now: decision.Now, ExpiresAt: decision.RequestedExpiresAt,
		})
		if err == nil {
			testcrashpoint.Hit("renew.after-store-cas")
			// Store 是租约事实源；提交后的 wake 只负责尽快刷新 lease.json。即使通知丢失，
			// 旧 timer 的 Store 复核和周期 scanner 仍会按新 expiry 恢复正确调度。
			if s.waker != nil && !testcrashpoint.Drop("wake.renew") {
				s.waker.Wake(updated.ID)
			}
			return updated, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return domain.Sandbox{}, fmt.Errorf("renew sandbox lease: %w", err)
		}
		if attempt == renewCASAttempts-1 {
			break
		}
		current, err = s.Get(ctx, command.SandboxID)
		if err != nil {
			return domain.Sandbox{}, err
		}
	}
	return domain.Sandbox{}, fmt.Errorf("renew sandbox lease: CAS retry exhausted: %w", domain.ErrConflict)
}
