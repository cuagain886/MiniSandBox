package application

import (
	"context"

	"minisandbox/internal/domain"
	"minisandbox/internal/store"
)

type SandboxService struct {
	store store.Store
}

func NewSandboxService(s store.Store) *SandboxService {
	return &SandboxService{store: s}
}

func (s *SandboxService) Get(ctx context.Context, id string) (domain.Sandbox, error) {
	return s.store.Get(ctx, id)
}
