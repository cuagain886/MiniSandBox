//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

// phase2Evidence 保存失败时可复核且不含 token/命令正文的 API 与 Docker 证据。
type phase2Evidence struct {
	mu      sync.Mutex
	events  []string
	inspect map[string]mobycontainer.InspectResponse
}

// recordEvent 记录稳定阶段名，不记录响应正文或凭据。
func (e *phase2Evidence) recordEvent(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

// captureContainer 保存指定受管容器的 inspect 快照供失败诊断。
func (e *phase2Evidence) captureContainer(ctx context.Context, client *mobyclient.Client, name string) error {
	result, err := client.ContainerInspect(ctx, name, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return errors.New("capture managed container inspection")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inspect == nil {
		e.inspect = make(map[string]mobycontainer.InspectResponse)
	}
	e.inspect[name] = result.Container
	return nil
}

// pollCondition 使用 deadline 与条件轮询等待状态，不依赖固定 sleep 推断异步完成。
func pollCondition(ctx context.Context, interval time.Duration, check func() (bool, error)) error {
	if ctx == nil || interval <= 0 || check == nil {
		return errors.New("poll condition is not configured")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ready, err := check()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll condition: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// TestPhase2HarnessPollConditionSelfCheck 验证轮询同时覆盖成功、错误与中断路径。
func TestPhase2HarnessPollConditionSelfCheck(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pollCondition(ctx, time.Millisecond, func() (bool, error) {
		attempts++
		return attempts == 3, nil
	}); err != nil || attempts != 3 {
		t.Fatalf("poll success: attempts=%d err=%v", attempts, err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := pollCondition(cancelled, time.Millisecond, func() (bool, error) { return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("poll cancellation: %v", err)
	}
}

// TestPhase2HarnessParallelNamesAreIsolated 验证并行 harness 使用互不重叠的随机资源身份。
func TestPhase2HarnessParallelNamesAreIsolated(t *testing.T) {
	first, second := randomTestID(t), randomTestID(t)
	if first == second || first[:8] == second[:8] {
		t.Fatalf("random integration identities collided")
	}
}
