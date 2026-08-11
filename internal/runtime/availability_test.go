package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAvailabilityGateClosesRecoversAndCancels 验证关闭、取消和恢复广播语义。
func TestAvailabilityGateClosesRecoversAndCancels(t *testing.T) {
	gate := NewAvailabilityGate(false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.WaitAvailable(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("closed gate wait: %v", err)
	}
	gate.SetAvailable(true)
	gate.SetAvailable(true)
	if err := gate.WaitAvailable(context.Background()); err != nil {
		t.Fatalf("recovered gate: %v", err)
	}
}
