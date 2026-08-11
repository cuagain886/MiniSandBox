package reconcile

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ReconcileFunc 对单个 sandbox ID 执行一次状态收敛。
//
// 实现必须响应 context 的取消和 deadline；返回错误只结束当前任务，不停止
// Phase 1 单 worker。
type ReconcileFunc func(context.Context, string) error

// ErrorReporter 接收单次 reconcile 的安全内部错误。
//
// reporter 应快速返回且不得阻塞 shutdown；Worker 不会把错误文本写入公共状态。
type ErrorReporter func(error)

// Worker 使用固定大小的池消费 WakeQueue，并为每次 reconcile 设置独立 timeout。
type Worker struct {
	queue     *WakeQueue
	workers   int
	timeout   time.Duration
	reconcile ReconcileFunc
	report    ErrorReporter
}

// NewWorker 创建兼容旧装配的单 worker。
//
// queue、reconcile 必须非空，timeout 必须为正数；report 可为空，表示调用方
// 暂不接收单次失败通知。
func NewWorker(
	queue *WakeQueue,
	timeout time.Duration,
	reconcile ReconcileFunc,
	report ErrorReporter,
) (*Worker, error) {
	return NewWorkerPool(queue, 1, timeout, reconcile, report)
}

// NewWorkerPool 创建固定并发度的 reconcile worker pool。
//
// workers 必须为正数；所有 worker 共享按 ID 合并的 WakeQueue，同一 ID 在完成前
// 不会被第二个 worker 取走。
func NewWorkerPool(
	queue *WakeQueue,
	workers int,
	timeout time.Duration,
	reconcile ReconcileFunc,
	report ErrorReporter,
) (*Worker, error) {
	if queue == nil {
		return nil, errors.New("wake queue must not be nil")
	}
	if timeout <= 0 {
		return nil, errors.New("reconcile timeout must be positive")
	}
	if workers <= 0 {
		return nil, errors.New("reconcile worker count must be positive")
	}
	if reconcile == nil {
		return nil, errors.New("reconcile function must not be nil")
	}
	return &Worker{
		queue:     queue,
		workers:   workers,
		timeout:   timeout,
		reconcile: reconcile,
		report:    report,
	}, nil
}

// Run 并发消费任务，直到 context 被取消且所有已开始任务都已返回。
//
// shutdown 后不再从队列取新 ID；正在执行的 reconcile 会收到取消信号，
// Run 等待它返回后才退出。普通错误和 panic 都不会终止后续任务。
func (w *Worker) Run(ctx context.Context) {
	var wait sync.WaitGroup
	wait.Add(w.workers)
	for workerID := 1; workerID <= w.workers; workerID++ {
		go func() {
			defer wait.Done()
			w.runOne(ctx)
		}()
	}
	wait.Wait()
}

// runOne 是单个固定 worker 的消费循环；panic 只隔离当前 item。
func (w *Worker) runOne(ctx context.Context) {
	for {
		sandboxID, err := w.queue.Next(ctx)
		if err != nil {
			return
		}
		if sandboxID == "" {
			continue
		}

		taskCtx, cancel := context.WithTimeout(ctx, w.timeout)
		taskErr := w.invoke(taskCtx, sandboxID)
		cancel()
		w.queue.Done(sandboxID)
		if taskErr != nil {
			w.reportError(taskErr)
		}
	}
}

// invoke 隔离单次调用的 panic，避免一个损坏任务杀死生命周期 worker。
func (w *Worker) invoke(ctx context.Context, sandboxID string) (err error) {
	defer func() {
		if recover() != nil {
			// panic value 可能含凭据或宿主机路径，不能进入错误文本或 reporter。
			err = &workerPanicError{}
		}
	}()
	return w.reconcile(ctx, sandboxID)
}

// reportError 隔离可选 reporter 的 panic，保持 worker 存活。
func (w *Worker) reportError(err error) {
	if w.report == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	w.report(err)
}

// workerPanicError 表示一次 reconcile 发生未恢复 panic。
type workerPanicError struct{}

// Error 返回固定安全文案，不包含 panic value 或堆栈。
func (*workerPanicError) Error() string {
	return "sandbox reconcile panicked"
}
