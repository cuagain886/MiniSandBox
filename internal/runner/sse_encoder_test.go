package runner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

type recordingSSEWriter struct {
	bytes.Buffer
	flushes  int
	flushErr error
}

func (w *recordingSSEWriter) Flush() error {
	w.flushes++
	return w.flushErr
}

// TestSSEEncoderWritesEveryEventType 验证所有合法事件都使用固定三字段 frame 并逐帧 flush。
func TestSSEEncoderWritesEveryEventType(t *testing.T) {
	exitCode, duration, truncated := 7, int64(25), false
	base := protocol.ExecutionEvent{ExecutionID: "exec_test", Sequence: 1, Timestamp: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	events := []protocol.ExecutionEvent{
		eventWithType(base, protocol.EventStarted),
		eventWithData(base, protocol.EventStdout, []byte("stdout")),
		eventWithData(base, protocol.EventStderr, []byte("stderr")),
		eventWithType(base, protocol.EventOutputLimitReached),
		eventWithTerminal(base, protocol.EventExited, &exitCode, duration, truncated),
		eventWithFailure(base, duration, truncated),
		eventWithTerminal(base, protocol.EventCancelled, nil, duration, truncated),
		eventWithTerminal(base, protocol.EventTimedOut, nil, duration, truncated),
	}
	for index, event := range events {
		event.Sequence = uint64(index + 1)
		writer := &recordingSSEWriter{}
		encoder, err := NewSSEEncoder(writer, writer)
		if err != nil {
			t.Fatalf("new encoder: %v", err)
		}
		if err := encoder.WriteEvent(event); err != nil {
			t.Fatalf("write %s: %v", event.Type, err)
		}
		frame := writer.String()
		wantPrefix := "id: " + string(rune('0'+index+1)) + "\nevent: " + string(event.Type) + "\ndata: "
		if !strings.HasPrefix(frame, wantPrefix) || !strings.HasSuffix(frame, "\n\n") || writer.flushes != 1 {
			t.Fatalf("frame %s: %q flushes=%d", event.Type, frame, writer.flushes)
		}
		dataLine := strings.TrimSuffix(strings.TrimPrefix(frame, wantPrefix), "\n\n")
		var decoded protocol.ExecutionEvent
		if err := json.Unmarshal([]byte(dataLine), &decoded); err != nil || decoded.Type != event.Type {
			t.Fatalf("data %s: decoded=%+v err=%v", event.Type, decoded, err)
		}
	}
}

// TestSSEEncoderPreventsFieldAndFrameInjection 验证特殊字符仅存在于单行 JSON 转义中，不能生成额外 SSE 字段。
func TestSSEEncoderPreventsFieldAndFrameInjection(t *testing.T) {
	event := eventWithData(protocol.ExecutionEvent{
		ExecutionID: "exec_\ndata: injected\r\nevent: fake",
		Sequence:    42,
		Timestamp:   time.Now().UTC(),
	}, protocol.EventStdout, []byte{'\n', '\r', 0, 0xff})
	writer := &recordingSSEWriter{}
	encoder, _ := NewSSEEncoder(writer, writer)
	if err := encoder.WriteEvent(event); err != nil {
		t.Fatalf("write event: %v", err)
	}
	lines := strings.Split(writer.String(), "\n")
	if len(lines) != 5 || lines[0] != "id: 42" || lines[1] != "event: stdout" || !strings.HasPrefix(lines[2], "data: {") || lines[3] != "" || lines[4] != "" {
		t.Fatalf("injected frame lines: %#v", lines)
	}
	if strings.Contains(lines[2], "\r") || strings.Contains(lines[2], "\ndata:") {
		t.Fatalf("JSON contains raw line break: %q", lines[2])
	}
}

// TestSSEEncoderKeepsBinaryOnlyInJSONBase64 验证二进制输出只以 Base64 JSON 字段出现。
func TestSSEEncoderKeepsBinaryOnlyInJSONBase64(t *testing.T) {
	raw := []byte{0xff, 0x00, '\n'}
	event := eventWithData(protocol.ExecutionEvent{ExecutionID: "exec_binary", Sequence: 1, Timestamp: time.Now().UTC()}, protocol.EventStdout, raw)
	writer := &recordingSSEWriter{}
	encoder, _ := NewSSEEncoder(writer, writer)
	if err := encoder.WriteEvent(event); err != nil {
		t.Fatalf("write event: %v", err)
	}
	if !strings.Contains(writer.String(), `"data_base64":"`+base64.StdEncoding.EncodeToString(raw)+`"`) || bytes.Contains(writer.Bytes(), raw) {
		t.Fatalf("binary frame: %q", writer.String())
	}
}

// TestSSEEncoderRejectsInvalidEventsWithoutWriting 验证 sequence 0、未知类型和非法字段组合均不会写半个 frame。
func TestSSEEncoderRejectsInvalidEventsWithoutWriting(t *testing.T) {
	base := protocol.ExecutionEvent{ExecutionID: "exec_invalid", Sequence: 1, Timestamp: time.Now().UTC(), Type: protocol.EventStarted}
	tests := []protocol.ExecutionEvent{
		{ExecutionID: base.ExecutionID, Timestamp: base.Timestamp, Type: protocol.EventStarted},
		{ExecutionID: base.ExecutionID, Sequence: 1, Timestamp: base.Timestamp, Type: "unknown"},
		{ExecutionID: base.ExecutionID, Sequence: 1, Timestamp: base.Timestamp, Type: protocol.EventStdout},
	}
	for _, event := range tests {
		writer := &recordingSSEWriter{}
		encoder, _ := NewSSEEncoder(writer, writer)
		if err := encoder.WriteEvent(event); !errors.Is(err, ErrInvalidSSEEvent) {
			t.Fatalf("invalid event error: %v", err)
		}
		if writer.Len() != 0 || writer.flushes != 0 {
			t.Fatalf("invalid event wrote data: %q", writer.String())
		}
	}
}

// TestSSEEncoderWritesKeepaliveAndDetectsShortWrite 验证 keepalive 不含 id/event，且短写被识别。
func TestSSEEncoderWritesKeepaliveAndDetectsShortWrite(t *testing.T) {
	writer := &recordingSSEWriter{}
	encoder, _ := NewSSEEncoder(writer, writer)
	if err := encoder.WriteKeepalive(); err != nil {
		t.Fatalf("write keepalive: %v", err)
	}
	if writer.String() != ": keepalive\n\n" || writer.flushes != 1 {
		t.Fatalf("keepalive: %q flushes=%d", writer.String(), writer.flushes)
	}
	short := &shortSSEWriter{}
	encoder, _ = NewSSEEncoder(short, short)
	if err := encoder.WriteEvent(protocol.ExecutionEvent{ExecutionID: "exec_short", Sequence: 1, Timestamp: time.Now().UTC(), Type: protocol.EventStarted}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
	flushFailure := errors.New("flush failed")
	failing := &recordingSSEWriter{flushErr: flushFailure}
	encoder, _ = NewSSEEncoder(failing, failing)
	if err := encoder.WriteKeepalive(); !errors.Is(err, flushFailure) {
		t.Fatalf("flush error: %v", err)
	}
}

type shortSSEWriter struct{}

func (*shortSSEWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }
func (*shortSSEWriter) Flush() error                   { return nil }

func eventWithType(base protocol.ExecutionEvent, eventType protocol.EventType) protocol.ExecutionEvent {
	base.Type = eventType
	return base
}

func eventWithData(base protocol.ExecutionEvent, eventType protocol.EventType, data []byte) protocol.ExecutionEvent {
	base.Type = eventType
	base.DataBase64 = base64.StdEncoding.EncodeToString(data)
	return base
}

func eventWithTerminal(base protocol.ExecutionEvent, eventType protocol.EventType, exitCode *int, duration int64, truncated bool) protocol.ExecutionEvent {
	base.Type = eventType
	base.ExitCode = exitCode
	base.DurationMS = &duration
	base.OutputTruncated = &truncated
	return base
}

func eventWithFailure(base protocol.ExecutionEvent, duration int64, truncated bool) protocol.ExecutionEvent {
	base = eventWithTerminal(base, protocol.EventFailed, nil, duration, truncated)
	base.ErrorCode = "INTERNAL_ERROR"
	base.Message = "execution failed"
	return base
}
