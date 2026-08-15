package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// writeSSEFrame 按控制面前台执行的实际格式写出一帧。
func writeSSEFrame(
	w http.ResponseWriter,
	event protocol.ExecutionEvent,
	overrideEventField string,
) {
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	eventField := string(event.Type)
	if overrideEventField != "" {
		eventField = overrideEventField
	}
	_, _ = fmt.Fprintf(
		w,
		"id: %d\nevent: %s\ndata: %s\n\n",
		event.Sequence,
		eventField,
		encoded,
	)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func streamEvent(sequence uint64, eventType protocol.EventType) protocol.ExecutionEvent {
	return protocol.ExecutionEvent{
		ExecutionID: "exec-stream",
		Sequence:    sequence,
		Timestamp:   time.Unix(1000+int64(sequence), 0).UTC(),
		Type:        eventType,
	}
}

// TestExecuteStreamDeliversDecodedEvents 验收前台 SSE：事件逐条解码、顺序
// 校验、终止事件后迭代自然结束。
func TestExecuteStreamDeliversDecodedEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("missing SSE accept header: %v", r.Header)
		}
		var request protocol.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode execute request: %v", err)
		}
		if request.Background {
			t.Error("ExecuteStream must request foreground execution")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		stdoutEvent := streamEvent(2, protocol.EventStdout)
		stdoutEvent.DataBase64 = "aGVsbG8="
		stderrEvent := streamEvent(3, protocol.EventStderr)
		stderrEvent.DataBase64 = "d29ybGQ="
		exitedEvent := streamEvent(4, protocol.EventExited)
		exitedEvent.ExitCode = &[]int{0}[0]
		exitedEvent.DurationMS = &[]int64{42}[0]
		exitedEvent.OutputTruncated = &[]bool{false}[0]

		writeSSEFrame(w, streamEvent(1, protocol.EventStarted), "")
		writeSSEFrame(w, stdoutEvent, "")
		writeSSEFrame(w, stderrEvent, "")
		writeSSEFrame(w, exitedEvent, "")
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Sandbox("sbx-stream").ExecuteStream(ctx, ExecuteRequest{
		Argv: []string{"/bin/sh", "-c", "echo hello"},
	})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}

	var types []EventType
	for stream.Next() {
		event := stream.Event()
		types = append(types, event.Type)
		if event.Type == EventStdout && string(event.Data) != "hello" {
			t.Fatalf("stdout not decoded: %q", event.Data)
		}
		if event.Type == EventStderr && string(event.Data) != "world" {
			t.Fatalf("stderr not decoded: %q", event.Data)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(types) != 4 || types[0] != EventStarted || types[3] != EventExited {
		t.Fatalf("unexpected stream events: %v", types)
	}
	if stream.Next() {
		t.Fatal("Next must stay false after the terminal event")
	}
}

// TestExecuteStreamProtocolFailures 验收流式协议防护：字段不一致、缺少终止
// 事件和非 200 响应都返回错误。
func TestExecuteStreamProtocolFailures(t *testing.T) {
	t.Run("event-field-disagreement", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter,
			_ *http.Request,
		) {
			w.Header().Set("Content-Type", "text/event-stream")
			stdoutEvent := streamEvent(1, protocol.EventStdout)
			stdoutEvent.DataBase64 = "aGk="
			writeSSEFrame(w, stdoutEvent, "stderr")
		}))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		stream, err := client.Sandbox("sbx-stream").ExecuteStream(
			context.Background(), ExecuteRequest{Argv: []string{"true"}},
		)
		if err != nil {
			t.Fatalf("execute stream: %v", err)
		}
		if stream.Next() {
			t.Fatal("disagreeing event field should fail the stream")
		}
		if stream.Err() == nil || !strings.Contains(stream.Err().Error(), "disagrees") {
			t.Fatalf("expected disagreement error, got %v", stream.Err())
		}
	})

	t.Run("missing-terminal-event", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter,
			_ *http.Request,
		) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeSSEFrame(w, streamEvent(1, protocol.EventStarted), "")
		}))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		stream, err := client.Sandbox("sbx-stream").ExecuteStream(
			context.Background(), ExecuteRequest{Argv: []string{"true"}},
		)
		if err != nil {
			t.Fatalf("execute stream: %v", err)
		}
		if stream.Next() != true {
			t.Fatal("first event should arrive")
		}
		if stream.Next() {
			t.Fatal("truncated stream should fail")
		}
		if stream.Err() == nil || !strings.Contains(stream.Err().Error(), "terminal") {
			t.Fatalf("expected missing terminal error, got %v", stream.Err())
		}
	})

	t.Run("non-200-response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter,
			_ *http.Request,
		) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{
					Code:      string(protocol.ErrorCodeSandboxNotRunning),
					Message:   "sandbox is not running",
					RequestID: "req-stream",
				},
			})
		}))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		_, err := client.Sandbox("sbx-stream").ExecuteStream(
			context.Background(), ExecuteRequest{Argv: []string{"true"}},
		)
		var responseError *ResponseError
		if !errors.As(err, &responseError) || !responseError.IsConflict() {
			t.Fatalf("expected conflict ResponseError, got %v", err)
		}
	})
}
