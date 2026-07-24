package reconcile

import "sync"

// KeyedLock 为每个 sandbox ID 提供独立互斥锁，避免同一资源并发收敛。
type KeyedLock struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewKeyedLock 创建空的按键互斥锁集合。
func NewKeyedLock() *KeyedLock {
	return &KeyedLock{locks: make(map[string]*sync.Mutex)}
}

// Lock 锁定指定键并返回解锁函数；调用方必须确保解锁函数恰好调用一次。
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
