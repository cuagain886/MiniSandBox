package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/application"
	"minisandbox/pkg/protocol"
)

func TestForegroundStreamCancellationClosesInternalStream(t *testing.T) {
	stream := newBlockingAPIStream()
	service := &apiExecutionServiceFake{result: application.ExecutionResult{Stream: stream}}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["sleep","10"]}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		NewRouter(BuildInfo{}, RouterDependencies{Execution: service, SSEWriteTimeout: time.Second}).ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	select {
	case <-stream.consuming:
	case <-time.After(time.Second):
		t.Fatal("stream did not start consuming")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("external cancellation did not close internal stream")
	}
	if stream.closeCount() == 0 {
		t.Fatal("internal stream was not closed")
	}
}

func TestForegroundStreamSetsPerFrameWriteDeadline(t *testing.T) {
	stream := &apiExecutionStreamFake{events: foregroundHandlerEvents()[:1]}
	service := &apiExecutionServiceFake{result: application.ExecutionResult{Stream: stream}}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["true"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	writer := &deadlineResponseWriter{header: make(http.Header), writeErr: errors.New("slow client")}
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service, SSEWriteTimeout: 25 * time.Millisecond}).ServeHTTP(writer, request)
	if len(writer.deadlines) == 0 || writer.deadlines[0].IsZero() {
		t.Fatalf("write deadline not set: %v", writer.deadlines)
	}
	if !stream.closed {
		t.Fatal("slow writer did not close internal stream")
	}
}

type blockingAPIStream struct {
	consuming chan struct{}
	closed    chan struct{}
	once      sync.Once
	mu        sync.Mutex
	closes    int
}

func newBlockingAPIStream() *blockingAPIStream {
	return &blockingAPIStream{consuming: make(chan struct{}), closed: make(chan struct{})}
}

func (s *blockingAPIStream) Consume(func(protocol.ExecutionEvent) error) error {
	close(s.consuming)
	<-s.closed
	return context.Canceled
}

func (s *blockingAPIStream) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *blockingAPIStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

type deadlineResponseWriter struct {
	header    http.Header
	deadlines []time.Time
	writeErr  error
}

func (w *deadlineResponseWriter) Header() http.Header       { return w.header }
func (*deadlineResponseWriter) WriteHeader(int)             {}
func (w *deadlineResponseWriter) Write([]byte) (int, error) { return 0, w.writeErr }
func (w *deadlineResponseWriter) FlushError() error         { return nil }
func (w *deadlineResponseWriter) SetWriteDeadline(value time.Time) error {
	w.deadlines = append(w.deadlines, value)
	return nil
}
