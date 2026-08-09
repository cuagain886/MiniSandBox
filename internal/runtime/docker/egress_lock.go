package docker

import "sync"

type egressAttachLock struct {
	mutex sync.Mutex
	refs  int
}

func (r *Runtime) lockEgressAttach(sandboxID string) func() {
	r.egressLocksMu.Lock()
	if r.egressLocks == nil {
		r.egressLocks = make(map[string]*egressAttachLock)
	}
	lock := r.egressLocks[sandboxID]
	if lock == nil {
		lock = &egressAttachLock{}
		r.egressLocks[sandboxID] = lock
	}
	lock.refs++
	r.egressLocksMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		r.egressLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.egressLocks, sandboxID)
		}
		r.egressLocksMu.Unlock()
	}
}
