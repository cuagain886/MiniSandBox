package runner

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrInvalidExecutionTimeout 表示 execution timeout 或默认值不能形成一个正数 deadline。
var ErrInvalidExecutionTimeout = errors.New("invalid execution timeout")

type timeoutTerminator interface {
	Terminate() error
}

type timeoutTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemTimeoutTimer struct {
	timer *time.Timer
}

func (t systemTimeoutTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimeoutTimer) Stop() bool          { return t.timer.Stop() }

type timeoutTimerFactory func(time.Duration) timeoutTimer

// ExecutionTimeout 从用户进程成功启动时开始计时，并仅在 deadline 赢得终态裁决时终止进程组。
// 请求排队、校验和启动准备时间不属于 execution duration。
type ExecutionTimeout struct {
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	mu  sync.RWMutex
	err error
}

// NewExecutionTimeout 创建并立即启动 execution deadline。requested 为零时使用 defaultTimeout；
// 调用方必须在 StartCommand 成功后传入 startedAt，避免把排队时间计入执行时长。
func NewExecutionTimeout(
	requested time.Duration,
	defaultTimeout time.Duration,
	startedAt time.Time,
	arbiter *TerminalArbiter,
	terminator timeoutTerminator,
) (*ExecutionTimeout, error) {
	return newExecutionTimeout(
		requested,
		defaultTimeout,
		startedAt,
		systemClock{},
		func(duration time.Duration) timeoutTimer {
			return systemTimeoutTimer{timer: time.NewTimer(duration)}
		},
		arbiter,
		terminator,
	)
}

func newExecutionTimeout(
	requested time.Duration,
	defaultTimeout time.Duration,
	startedAt time.Time,
	clock Clock,
	newTimer timeoutTimerFactory,
	canSubmit *TerminalArbiter,
	terminator timeoutTerminator,
) (*ExecutionTimeout, error) {
	timeout := requested
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout <= 0 || startedAt.IsZero() || clock == nil || newTimer == nil || canSubmit == nil || terminator == nil {
		return nil, ErrInvalidExecutionTimeout
	}
	timer := newTimer(timeout)
	if timer == nil || timer.C() == nil {
		return nil, ErrInvalidExecutionTimeout
	}
	source := &ExecutionTimeout{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go source.run(startedAt, clock, timer, canSubmit, terminator)
	return source, nil
}

// Stop 在 execution 已由其他路径收尾时停止并释放 timer；重复调用保持幂等。
func (t *ExecutionTimeout) Stop() error {
	if t == nil || t.stop == nil || t.done == nil {
		return ErrInvalidExecutionTimeout
	}
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done
	return t.result()
}

// Wait 等待 timeout goroutine 退出；它不会修改调用方 context，也不充当 HTTP 请求超时。
func (t *ExecutionTimeout) Wait(ctx context.Context) error {
	if t == nil || t.done == nil {
		return ErrInvalidExecutionTimeout
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return t.result()
	}
}

func (t *ExecutionTimeout) run(startedAt time.Time, clock Clock, timer timeoutTimer, arbiter *TerminalArbiter, terminator timeoutTerminator) {
	defer close(t.done)
	defer timer.Stop()
	select {
	case <-t.stop:
		return
	case <-timer.C():
	}
	duration := clock.Now().Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	decision, err := arbiter.Submit(context.Background(), TerminalCandidate{
		Reason:   TerminationDeadlineExceeded,
		Duration: duration,
	})
	if err != nil {
		t.setResult(errors.New("submit execution timeout failed"))
		return
	}
	// timeout 败选后不得触碰进程组，否则会把已经归属于 exit/cancel 的执行错误地杀死。
	if decision.Won {
		t.setResult(terminator.Terminate())
	}
}

func (t *ExecutionTimeout) setResult(err error) {
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
}

func (t *ExecutionTimeout) result() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.err
}
