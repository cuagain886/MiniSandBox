package runnerclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

func TestDecodeSSEFragmentedCRLFAndKeepalive(t *testing.T) {
	events := validStreamEvents()
	stream := ": keepalive\r\n\r\n" + encodeClientFrames(t, events, "\r\n")
	reader := &fragmentReader{data: []byte(stream), size: 3}
	var got []protocol.ExecutionEvent
	if err := DecodeSSE(reader, func(event protocol.ExecutionEvent) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatalf("decode SSE: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("event count: got %d, want %d", len(got), len(events))
	}
	for index := range events {
		if got[index].Sequence != events[index].Sequence || got[index].Type != events[index].Type {
			t.Fatalf("event %d mismatch: %+v", index, got[index])
		}
	}
}

func TestDecodeSSERejectsProtocolViolationsAndCloses(t *testing.T) {
	events := validStreamEvents()
	valid := encodeClientFrames(t, events, "\n")
	tests := map[string]string{
		"sequence gap":       strings.Replace(valid, "id: 2", "id: 3", 1),
		"field disagreement": strings.Replace(valid, "event: stdout", "event: stderr", 1),
		"execution mismatch": strings.Replace(valid, `"execution_id":"exec_test"`, `"execution_id":"exec_other"`, 1),
		"unknown JSON":       strings.Replace(valid, `"sequence":1`, `"unknown":true,"sequence":1`, 1),
		"double terminal":    valid + encodeClientFrames(t, events[len(events)-1:], "\n"),
		"missing terminal":   encodeClientFrames(t, events[:len(events)-1], "\n"),
		"post terminal":      encodeClientFrames(t, append(events, events[1]), "\n"),
		"unknown field":      "retry: 5\n\n" + valid,
		"oversized line":     "data: " + strings.Repeat("x", maxSSELineBytes) + "\n\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			body := &trackingReadCloser{Reader: strings.NewReader(stream)}
			err := DecodeSSE(body, func(protocol.ExecutionEvent) error { return nil })
			var mismatch *ProtocolMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("error: got %v, want protocol mismatch", err)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestDecodeSSEPropagatesConsumerErrorAndCloses(t *testing.T) {
	want := errors.New("stop")
	body := &trackingReadCloser{Reader: strings.NewReader(encodeClientFrames(t, validStreamEvents(), "\n"))}
	err := DecodeSSE(body, func(protocol.ExecutionEvent) error { return want })
	if !errors.Is(err, want) || !body.closed {
		t.Fatalf("consumer result: err=%v closed=%v", err, body.closed)
	}
}

func TestDecodeSSEAcceptsBoundedLargeOutputFrame(t *testing.T) {
	events := validStreamEvents()
	events[1].DataBase64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32*1024))
	if err := DecodeSSE(strings.NewReader(encodeClientFrames(t, events, "\n")), func(protocol.ExecutionEvent) error { return nil }); err != nil {
		t.Fatalf("decode bounded large frame: %v", err)
	}
}

func TestDecodeSSEAcceptsEveryTerminalType(t *testing.T) {
	base := validStreamEvents()
	for _, eventType := range []protocol.EventType{protocol.EventExited, protocol.EventFailed, protocol.EventCancelled, protocol.EventTimedOut} {
		t.Run(string(eventType), func(t *testing.T) {
			duration, truncated := int64(1), false
			terminal := protocol.ExecutionEvent{ExecutionID: "exec_test", Sequence: uint64(len(base)), Timestamp: base[0].Timestamp, Type: eventType, DurationMS: &duration, OutputTruncated: &truncated}
			switch eventType {
			case protocol.EventExited:
				exitCode := 0
				terminal.ExitCode = &exitCode
			case protocol.EventFailed:
				terminal.ErrorCode, terminal.Message = "INTERNAL_ERROR", "execution failed"
			}
			events := append(append([]protocol.ExecutionEvent(nil), base[:len(base)-1]...), terminal)
			if err := DecodeSSE(strings.NewReader(encodeClientFrames(t, events, "\n")), func(protocol.ExecutionEvent) error { return nil }); err != nil {
				t.Fatalf("decode terminal %s: %v", eventType, err)
			}
		})
	}
}

func validStreamEvents() []protocol.ExecutionEvent {
	now := time.Date(2026, 8, 7, 1, 2, 3, 0, time.UTC)
	duration, truncated, exitCode := int64(9), false, 0
	return []protocol.ExecutionEvent{
		{ExecutionID: "exec_test", Sequence: 1, Timestamp: now, Type: protocol.EventStarted},
		{ExecutionID: "exec_test", Sequence: 2, Timestamp: now, Type: protocol.EventStdout, DataBase64: base64.StdEncoding.EncodeToString([]byte("ok"))},
		{ExecutionID: "exec_test", Sequence: 3, Timestamp: now, Type: protocol.EventStderr, DataBase64: base64.StdEncoding.EncodeToString([]byte("warn"))},
		{ExecutionID: "exec_test", Sequence: 4, Timestamp: now, Type: protocol.EventOutputLimitReached},
		{ExecutionID: "exec_test", Sequence: 5, Timestamp: now, Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: &duration, OutputTruncated: &truncated},
	}
}

func encodeClientFrames(t *testing.T, events []protocol.ExecutionEvent, newline string) string {
	t.Helper()
	var result bytes.Buffer
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		result.WriteString("id: " + strconv.FormatUint(event.Sequence, 10) + newline)
		result.WriteString("event: " + string(event.Type) + newline)
		result.WriteString("data: " + string(data) + newline + newline)
	}
	return result.String()
}

type fragmentReader struct {
	data []byte
	size int
}

func (r *fragmentReader) Read(target []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	count := r.size
	if count > len(target) {
		count = len(target)
	}
	if count > len(r.data) {
		count = len(r.data)
	}
	copy(target, r.data[:count])
	r.data = r.data[count:]
	return count, nil
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
