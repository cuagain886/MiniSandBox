package reconcile

import (
	"context"
	"sync"
)

// KeyedLock 为每个 sandbox ID 提供可取消等待的独立互斥锁。
type KeyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	token   chan struct{}
	holders int
	waiters int
}

// NewKeyedLock 创建空的按键互斥锁集合。
func NewKeyedLock() *KeyedLock {
	return &KeyedLock{entries: make(map[string]*keyedLockEntry)}
}

// Lock 锁定指定 key；它保留旧调用面并使用不可取消的 background context。
func (k *KeyedLock) Lock(key string) func() {
	unlock, _ := k.LockContext(context.Background(), key)
	return unlock
}

// LockContext 等待指定 key；context 取消时回收 waiter 并返回原始错误。
func (k *KeyedLock) LockContext(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k.mu.Lock()
	entry := k.entries[key]
	if entry == nil {
		entry = &keyedLockEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		k.entries[key] = entry
	}
	entry.waiters++
	k.mu.Unlock()

	select {
	case <-ctx.Done():
		k.cancelWaiter(key, entry)
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			k.cancelWaiter(key, entry)
			return nil, err
		}
	}
	k.mu.Lock()
	entry.waiters--
	entry.holders++
	k.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			k.mu.Lock()
			entry.holders--
			k.deleteIdleEntry(key, entry)
			k.mu.Unlock()
		})
	}, nil
}

func (k *KeyedLock) cancelWaiter(key string, entry *keyedLockEntry) {
	k.mu.Lock()
	entry.waiters--
	k.deleteIdleEntry(key, entry)
	k.mu.Unlock()
}

func (k *KeyedLock) deleteIdleEntry(key string, entry *keyedLockEntry) {
	// 指针身份校验防止旧 entry 的迟到 release/cancel 误删同 key 后来重建的 entry。
	if entry.holders == 0 && entry.waiters == 0 && k.entries[key] == entry {
		delete(k.entries, key)
	}
}

func (k *KeyedLock) entryCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.entries)
}
