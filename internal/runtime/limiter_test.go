package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLimiterBlocksAtCapacityAndCancels 验证满载等待不会偷占配额且可取消。
func TestLimiterBlocksAtCapacityAndCancels(t *testing.T) {
	limiter, err := NewLimiter(1)
	if err != nil {
		t.Fatalf("new limiter: %v", err)
	}
	release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire: got %v, want deadline", err)
	}
	release()
	release()
	if secondRelease, err := limiter.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		secondRelease()
	}
}

// TestNewLimiterRejectsInvalidCapacity 验证零或负容量不会制造永久阻塞门禁。
func TestNewLimiterRejectsInvalidCapacity(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := NewLimiter(limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}
}
