package application

import (
	"context"
	"errors"
	"fmt"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
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
		updated, err := s.store.Renew(ctx, storeport.RenewUpdate{
			ID: current.ID, ExpectedRevision: current.Revision,
			Now: decision.Now, ExpiresAt: decision.RequestedExpiresAt,
		})
		if err == nil {
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
