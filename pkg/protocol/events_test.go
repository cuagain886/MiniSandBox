package protocol

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func testEventTime() time.Time {
	return time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
}

// TestExecutionEventVariants 验证八种事件及四种互斥终态。
func TestExecutionEventVariants(t *testing.T) {
	exitCode, duration, truncated := 9, int64(15), false
	base := ExecutionEvent{ExecutionID: "e1", Sequence: 1, Timestamp: testEventTime()}
	events := []ExecutionEvent{
		withEventType(base, EventStarted),
		withEventData(base, EventStdout, []byte{0xff, 0x00, '\n'}),
		withEventData(base, EventStderr, []byte("warning\n")),
		withEventType(base, EventOutputLimitReached),
		withTerminal(base, EventExited, &exitCode, duration, truncated),
		withFailure(base, "START_FAILED", "Execution could not be started.", duration, truncated),
		withTerminal(base, EventCancelled, nil, duration, truncated),
		withTerminal(base, EventTimedOut, nil, duration, truncated),
	}
	for _, event := range events {
		if err := event.Validate(); err != nil {
			t.Errorf("%s should be valid: %v", event.Type, err)
		}
		if got := event.Terminal(); got != (event.Type == EventExited ||
			event.Type == EventFailed || event.Type == EventCancelled ||
			event.Type == EventTimedOut) {
			t.Errorf("unexpected terminal result for %s: %t", event.Type, got)
		}
	}
	encoded, err := json.Marshal(events[1])
	if err != nil {
		t.Fatalf("marshal binary event: %v", err)
	}
	var decoded ExecutionEvent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal binary event: %v", err)
	}
	bytes, err := base64.StdEncoding.DecodeString(decoded.DataBase64)
	if err != nil || string(bytes) != string([]byte{0xff, 0x00, '\n'}) {
		t.Fatalf("Base64 round trip: bytes=%v err=%v", bytes, err)
	}
}

// TestExecutionEventRejectsIllegalFieldCombinations 验证终态专属字段不能串用。
func TestExecutionEventRejectsIllegalFieldCombinations(t *testing.T) {
	exitCode, duration, truncated := 0, int64(1), false
	base := ExecutionEvent{ExecutionID: "e1", Sequence: 1, Timestamp: testEventTime()}
	tests := []ExecutionEvent{
		withEventType(ExecutionEvent{}, EventStarted),
		withEventData(base, EventStdout, []byte{}),
		withTerminal(base, EventExited, nil, duration, truncated),
		withTerminal(base, EventCancelled, &exitCode, duration, truncated),
		withFailure(base, "", "unsafe detail", duration, truncated),
		{ExecutionID: "e1", Sequence: 1, Timestamp: testEventTime(), Type: EventStdout, DataBase64: "%%%"},
	}
	for index, event := range tests {
		if err := event.Validate(); err == nil {
			t.Errorf("case %d unexpectedly valid: %#v", index, event)
		}
	}
}

func withEventType(base ExecutionEvent, eventType EventType) ExecutionEvent {
	base.Type = eventType
	return base
}

func withEventData(base ExecutionEvent, eventType EventType, data []byte) ExecutionEvent {
	base.Type = eventType
	base.DataBase64 = base64.StdEncoding.EncodeToString(data)
	return base
}

func withTerminal(base ExecutionEvent, eventType EventType, exitCode *int, duration int64, truncated bool) ExecutionEvent {
	base.Type = eventType
	base.ExitCode = exitCode
	base.DurationMS = &duration
	base.OutputTruncated = &truncated
	return base
}

func withFailure(base ExecutionEvent, code, message string, duration int64, truncated bool) ExecutionEvent {
	base = withTerminal(base, EventFailed, nil, duration, truncated)
	base.ErrorCode = code
	base.Message = message
	return base
}
