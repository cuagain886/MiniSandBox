package reconcile

import (
	"context"
	"errors"
	"sync"
)

// ErrWakeQueueClosed 表示 shutdown 已关闭新唤醒和任务领取。
var ErrWakeQueueClosed = errors.New("wake queue is closed")

// WakeQueue 按 sandbox ID 合并尚未开始处理的 reconcile 唤醒。
//
// notification 只表达“队列可能非空”，容量固定为 1；真实任务保存在 states
// 和 order 中，因此通知已满不会丢失 ID，同一 ID 高频 Wake 也只占一个状态项。
// processing 身份保存在每个 ID 的状态中，因此多个 worker 可并发取走不同 ID。
type WakeQueue struct {
	mu           sync.Mutex
	states       map[string]wakeState
	order        []string
	notification chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	isClosed     bool
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
		closed:       make(chan struct{}),
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
	if q.isClosed {
		q.mu.Unlock()
		return false
	}
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
	case <-q.closed:
		return "", ErrWakeQueueClosed
	case <-q.notification:
	}
	if err := ctx.Err(); err != nil {
		// select 同时观察到任务与 shutdown 时可能选择任务分支；把通知放回，
		// 确保取消后的 worker 不会取走尚未开始的任务。
		q.notify()
		return "", err
	}

	q.mu.Lock()
	if q.isClosed {
		q.mu.Unlock()
		return "", ErrWakeQueueClosed
	}
	if len(q.order) == 0 {
		q.mu.Unlock()
		return "", nil
	}
	sandboxID := q.order[0]
	q.order[0] = ""
	q.order = q.order[1:]
	q.states[sandboxID] = wakeProcessing
	hasPending := len(q.order) > 0
	q.mu.Unlock()
	// 单槽 notification 只唤醒一个 waiter；仍有任务时立即补回令牌，使其他
	// pool worker 可以并发取走不同 ID。
	if hasPending {
		q.notify()
	}
	return sandboxID, nil
}

// Close 永久停止接收 Wake 并阻止 worker 领取尚未开始的任务。
// 已在处理中的 ID 由 worker context 负责取消；重复调用是幂等 no-op。
func (q *WakeQueue) Close() {
	if q == nil {
		return
	}
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.isClosed = true
		q.mu.Unlock()
		close(q.closed)
	})
}

// Done 通知队列指定任务已经处理完成。
//
// 若处理期间当前 ID 被再次 Wake，Done 会把它放回队尾；未知或已完成 ID
// 是幂等 no-op，不能影响其他 worker 正在处理的身份。
func (q *WakeQueue) Done(sandboxID string) {
	if q == nil || sandboxID == "" {
		return
	}
	q.mu.Lock()
	switch q.states[sandboxID] {
	case wakeRequeue:
		q.states[sandboxID] = wakePending
		q.order = append(q.order, sandboxID)
	case wakeProcessing:
		delete(q.states, sandboxID)
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
	for _, state := range q.states {
		if state == wakeRequeue {
			length++
		}
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
