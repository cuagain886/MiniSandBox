package reconcile

import "sync"

type KeyedLock struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewKeyedLock() *KeyedLock {
	return &KeyedLock{locks: make(map[string]*sync.Mutex)}
}

func (k *KeyedLock) Lock(key string) func() {
	k.mu.Lock()
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	k.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
