package application

import (
	"context"

	"minisandbox/internal/domain"
)

type ExecutionGateway interface {
	Execute(context.Context, string, domain.ExecutionSpec) error
	Cancel(context.Context, string, string) error
}

type ExecutionService struct {
	gateway ExecutionGateway
}

func NewExecutionService(gateway ExecutionGateway) *ExecutionService {
	return &ExecutionService{gateway: gateway}
}

func (s *ExecutionService) Execute(ctx context.Context, command Execute) error {
	if !command.Spec.Valid() {
		return domain.ErrInvalid
	}
	return s.gateway.Execute(ctx, command.SandboxID, command.Spec)
}
