package runner

import (
	"errors"
	"sync"
	"time"
)

// ErrProcessGroupTerminationFailed 表示无法安全确认目标进程组已经消失。
var ErrProcessGroupTerminationFailed = errors.New("execution process group termination failed")

// ProcessGroupTerminator 对构造时固定的单个 PGID 执行幂等终止，不能改为其他进程或进程组。
type ProcessGroupTerminator struct {
	once      sync.Once
	done      chan struct{}
	terminate func() error
	err       error
}

// NewProcessGroupTerminator 创建 TERM→grace→KILL terminator；reaped 由唯一 waiter 完成后关闭。
func NewProcessGroupTerminator(pgid int, grace time.Duration, reaped <-chan struct{}) (*ProcessGroupTerminator, error) {
	return newPlatformProcessGroupTerminator(pgid, grace, reaped)
}

// Terminate 执行或等待同一次终止流程；并发和重复调用返回相同结果且不重复发送信号。
func (t *ProcessGroupTerminator) Terminate() error {
	if t == nil || t.terminate == nil || t.done == nil {
		return ErrProcessGroupTerminationFailed
	}
	t.once.Do(func() {
		t.err = t.terminate()
		close(t.done)
	})
	<-t.done
	return t.err
}
