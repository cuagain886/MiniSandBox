package reconcile

import (
	"context"
	"sync"
)

// WakeQueue 按 sandbox ID 合并尚未开始处理的 reconcile 唤醒。
//
// notification 只表达“队列可能非空”，容量固定为 1；真实任务保存在 states
// 和 order 中，因此通知已满不会丢失 ID，同一 ID 高频 Wake 也只占一个状态项。
// 当前显式保持单 worker processing 身份。
type WakeQueue struct {
	mu           sync.Mutex
	states       map[string]wakeState
	order        []string
	processing   string
	notification chan struct{}
}

type wakeState uint8

const (
	wakePending wakeState = iota + 1
	wakeProcessing
	wakeRequeue
)

// NewWakeQueue 创建空的按 ID 合并队列。
func NewWakeQueue() *WakeQueue {
	return &WakeQueue{
		states:       make(map[string]wakeState),
		notification: make(chan struct{}, 1),
	}
}

// Wake 把 sandbox ID 加入待处理集合。
//
// 返回 true 表示新增 pending 或把 processing 提升为 requeue；空 ID、已经
// pending 或已经 requeue 的 ID 返回 false。本方法不阻塞。
func (q *WakeQueue) Wake(sandboxID string) bool {
	if q == nil || sandboxID == "" {
		return false
	}
	q.mu.Lock()
	state := q.states[sandboxID]
	switch state {
	case wakePending, wakeRequeue:
		q.mu.Unlock()
		return false
	case wakeProcessing:
		q.states[sandboxID] = wakeRequeue
		q.mu.Unlock()
		return true
	}
	q.states[sandboxID] = wakePending
	q.order = append(q.order, sandboxID)
	q.mu.Unlock()
	q.notify()
	return true
}

// Next 等待并返回一个待处理 sandbox ID。
//
// ID 在返回前从 pending 转为 processing；处理期间再次 Wake 只提升为一次
// requeue。context 取消时不取走新任务，并返回 context 的原始错误。
func (q *WakeQueue) Next(ctx context.Context) (string, error) {
	if q == nil {
		<-ctx.Done()
		return "", ctx.Err()
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-q.notification:
	}
	if err := ctx.Err(); err != nil {
		// select 同时观察到任务与 shutdown 时可能选择任务分支；把通知放回，
		// 确保取消后的 worker 不会取走尚未开始的任务。
		q.notify()
		return "", err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return "", nil
	}
	sandboxID := q.order[0]
	q.order[0] = ""
	q.order = q.order[1:]
	q.states[sandboxID] = wakeProcessing
	q.processing = sandboxID
	return sandboxID, nil
}

// Done 通知队列当前任务已经处理完成。
//
// 若处理期间积累了其他 ID 或当前 ID 被再次 Wake，Done 会重新发出一个
// 非阻塞通知，使单 worker 继续消费；队列为空时是幂等 no-op。
func (q *WakeQueue) Done() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.processing != "" {
		id := q.processing
		switch q.states[id] {
		case wakeRequeue:
			q.states[id] = wakePending
			q.order = append(q.order, id)
		case wakeProcessing:
			delete(q.states, id)
		}
		q.processing = ""
	}
	hasPending := len(q.order) > 0
	q.mu.Unlock()
	if hasPending {
		q.notify()
	}
}

// Len 返回当前合并后的待处理 ID 数量，不包含 worker 正在处理的任务。
func (q *WakeQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	length := len(q.order)
	if q.processing != "" && q.states[q.processing] == wakeRequeue {
		length++
	}
	return length
}

// notify 非阻塞表达队列非空；任务事实始终由 pending/order 保存。
func (q *WakeQueue) notify() {
	select {
	case q.notification <- struct{}{}:
	default:
	}
}
