package reconcile

import (
	"context"

	runtimeport "minisandbox/internal/runtime"
	"minisandbox/internal/store"
)

type Reconciler struct {
	store   store.Store
	runtime runtimeport.Runtime
	locks   *KeyedLock
}

func New(s store.Store, r runtimeport.Runtime) *Reconciler {
	return &Reconciler{store: s, runtime: r, locks: NewKeyedLock()}
}

func (r *Reconciler) Reconcile(context.Context, string) error {
	return nil
}
