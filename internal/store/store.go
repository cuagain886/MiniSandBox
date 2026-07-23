package store

import (
	"context"

	"minisandbox/internal/domain"
)

type Store interface {
	Save(context.Context, domain.Sandbox) error
	Get(context.Context, string) (domain.Sandbox, error)
	List(context.Context) ([]domain.Sandbox, error)
	Delete(context.Context, string) error
}
