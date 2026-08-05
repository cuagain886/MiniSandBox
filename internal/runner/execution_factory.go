package runner

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	executionIDPrefix      = "exec_"
	executionIDRandomBytes = 16
)

// Clock 是 execution factory 获取时间的唯一入口。
type Clock interface {
	// Now 返回当前时间；factory 会把结果规范化为 UTC。
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

// ExecutionFactory 通过加密随机源和注入时钟创建 Pending execution。
type ExecutionFactory struct {
	mu     sync.Mutex
	random io.Reader
	clock  Clock
}

// NewExecutionFactory 创建使用系统加密随机源和系统时钟的 production factory。
func NewExecutionFactory() *ExecutionFactory {
	return newExecutionFactory(rand.Reader, systemClock{})
}

func newExecutionFactory(random io.Reader, clock Clock) *ExecutionFactory {
	return &ExecutionFactory{random: random, clock: clock}
}

// New 创建带不可预测 URL-safe ID 和 UTC 创建时间的 Pending execution。
// 随机源失败时不返回部分 ID，也不创建 execution。
func (f *ExecutionFactory) New() (*Execution, error) {
	if f == nil || f.random == nil || f.clock == nil {
		return nil, errors.New("execution factory is not configured")
	}
	randomBytes := make([]byte, executionIDRandomBytes)
	defer clear(randomBytes)

	// 注入 reader 和 clock 不一定并发安全；统一加锁也保证一次创建使用同一次完整随机读取。
	f.mu.Lock()
	_, err := io.ReadFull(f.random, randomBytes)
	if err != nil {
		f.mu.Unlock()
		return nil, errors.New("generate execution ID failed")
	}
	createdAt := f.clock.Now().UTC()
	f.mu.Unlock()
	if createdAt.IsZero() {
		return nil, errors.New("execution clock returned zero time")
	}

	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(randomBytes)))
	defer clear(encoded)
	base64.RawURLEncoding.Encode(encoded, randomBytes)
	id := ExecutionID(executionIDPrefix + string(encoded))
	return newPendingExecution(id, createdAt), nil
}
