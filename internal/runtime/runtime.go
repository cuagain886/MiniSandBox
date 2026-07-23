package runtime

import (
	"context"

	"minisandbox/internal/domain"
)

type Runtime interface {
	Ensure(context.Context, domain.Sandbox) (ActualSandbox, error)
	Inspect(context.Context, string) (ActualSandbox, error)
	Delete(context.Context, string) error
	ListManaged(context.Context) ([]ActualSandbox, error)
}
