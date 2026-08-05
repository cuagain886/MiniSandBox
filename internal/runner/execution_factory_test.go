package runner

import (
	"bytes"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

var executionIDPattern = regexp.MustCompile(`^exec_[A-Za-z0-9_-]{22}$`)

type fixedClock struct {
	value time.Time
}

func (c fixedClock) Now() time.Time {
	return c.value
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("random unavailable")
}

// TestExecutionFactoryGeneratesURLSafeIDAndUTCTime 验证固定随机输入的格式及注入时钟的 UTC 规范化。
func TestExecutionFactoryGeneratesURLSafeIDAndUTCTime(t *testing.T) {
	randomBytes := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	local := time.Date(2026, 8, 5, 9, 30, 0, 123, time.FixedZone("test", 8*60*60))
	factory := newExecutionFactory(bytes.NewReader(randomBytes), fixedClock{value: local})
	execution, err := factory.New()
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	descriptor := execution.Descriptor()
	if descriptor.ID != "exec_AAECAwQFBgcICQoLDA0ODw" || !executionIDPattern.MatchString(string(descriptor.ID)) {
		t.Fatalf("execution ID: %q", descriptor.ID)
	}
	if descriptor.CreatedAt.Location() != time.UTC || !descriptor.CreatedAt.Equal(local) {
		t.Fatalf("created at: got %v, want UTC %v", descriptor.CreatedAt, local.UTC())
	}
	if descriptor.State != ExecutionPending {
		t.Fatalf("initial state: %q", descriptor.State)
	}
}

// TestExecutionFactoryRejectsRandomAndClockFailure 验证熵不足与零时钟均不会创建 execution。
func TestExecutionFactoryRejectsRandomAndClockFailure(t *testing.T) {
	if execution, err := newExecutionFactory(failingReader{}, fixedClock{value: time.Now()}).New(); err == nil || execution != nil {
		t.Fatalf("random failure accepted: execution=%v err=%v", execution, err)
	}
	if execution, err := newExecutionFactory(bytes.NewReader(make([]byte, executionIDRandomBytes)), fixedClock{}).New(); err == nil || execution != nil {
		t.Fatalf("zero clock accepted: execution=%v err=%v", execution, err)
	}
	if execution, err := (*ExecutionFactory)(nil).New(); err == nil || execution != nil {
		t.Fatalf("nil factory accepted: execution=%v err=%v", execution, err)
	}
}

// TestExecutionFactoryGeneratesUniqueIDsConcurrently 使用 production 熵源并发生成 ID，验证无共享缓冲竞态或重复。
func TestExecutionFactoryGeneratesUniqueIDsConcurrently(t *testing.T) {
	const count = 256
	factory := NewExecutionFactory()
	ids := make(chan ExecutionID, count)
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			execution, err := factory.New()
			if err != nil {
				errorsFound <- err
				return
			}
			ids <- execution.Descriptor().ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent generation: %v", err)
	}
	seen := make(map[ExecutionID]struct{}, count)
	for id := range ids {
		if !executionIDPattern.MatchString(string(id)) {
			t.Fatalf("unsafe ID: %q", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate ID: %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("ID count: got %d, want %d", len(seen), count)
	}
}
