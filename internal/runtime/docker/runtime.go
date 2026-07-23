package docker

import (
	"context"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

type Runtime struct{}

func New() *Runtime {
	return &Runtime{}
}

func (r *Runtime) Ensure(
	context.Context,
	domain.Sandbox,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, domain.ErrNotImplemented
}

func (r *Runtime) Inspect(
	context.Context,
	string,
) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, domain.ErrNotImplemented
}

func (r *Runtime) Delete(context.Context, string) error {
	return domain.ErrNotImplemented
}

func (r *Runtime) ListManaged(
	context.Context,
) ([]runtimeport.ActualSandbox, error) {
	return nil, domain.ErrNotImplemented
}

var _ runtimeport.Runtime = (*Runtime)(nil)
