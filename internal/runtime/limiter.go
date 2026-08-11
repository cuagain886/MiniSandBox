package runtime

import (
	"context"
	"errors"
	"sync"
)

// Limiter 是可取消的共享并发门禁。
//
// Acquire 成功返回必须调用一次的 release；等待期间 context 取消时不得占用
// 配额。实现不携带 sandbox 或镜像身份，避免形成无界 keyed lock map。
type Limiter interface {
	Acquire(context.Context) (release func(), err error)
}

// NewLimiter 创建固定容量的进程内并发门禁。
func NewLimiter(limit int) (Limiter, error) {
	if limit <= 0 {
		return nil, errors.New("concurrency limit must be positive")
	}
	return &fixedLimiter{slots: make(chan struct{}, limit)}, nil
}

type fixedLimiter struct {
	slots chan struct{}
}

func (l *fixedLimiter) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.slots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
