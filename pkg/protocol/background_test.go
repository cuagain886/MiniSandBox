package protocol

import (
	"encoding/json"
	"testing"
)

// TestBackgroundModelsJSONRoundTrip 固定 descriptor、status 和 logs page 字段。
func TestBackgroundModelsJSONRoundTrip(t *testing.T) {
	exitCode, duration, truncated := 7, int64(25), false
	terminal := ExecutionEvent{
		ExecutionID:     "e1",
		Sequence:        3,
		Timestamp:       testEventTime(),
		Type:            EventExited,
		ExitCode:        &exitCode,
		DurationMS:      &duration,
		OutputTruncated: &truncated,
	}
	values := []any{
		ExecutionDescriptor{ExecutionID: "e1", State: ExecutionStatePending},
		ExecutionStatus{ExecutionID: "e1", State: ExecutionStateExited, TerminalEvent: &terminal},
		ExecutionLogPage{Events: []ExecutionEvent{terminal}, NextCursor: 3, Complete: true},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid JSON for %T: %s", value, encoded)
		}
	}
}
