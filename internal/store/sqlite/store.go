package sqlite

import (
	"context"

	"minisandbox/internal/domain"
	storeport "minisandbox/internal/store"
)

type Store struct {
	path string
}

func Open(path string) (*Store, error) {
	return &Store{path: path}, nil
}

func (s *Store) Save(context.Context, domain.Sandbox) error {
	return domain.ErrNotImplemented
}

func (s *Store) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, domain.ErrNotImplemented
}

func (s *Store) List(context.Context) ([]domain.Sandbox, error) {
	return nil, domain.ErrNotImplemented
}

func (s *Store) Delete(context.Context, string) error {
	return domain.ErrNotImplemented
}

var _ storeport.Store = (*Store)(nil)
