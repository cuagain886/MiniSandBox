package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

const executionHandlerSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

func TestForegroundExecutionHandlerMapsRequestAndForwardsSSE(t *testing.T) {
	events := foregroundHandlerEvents()
	stream := &apiExecutionStreamFake{events: events}
	service := &apiExecutionServiceFake{result: application.ExecutionResult{Stream: stream}}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["printf","ok"],"env":{"A":"B"},"cwd":"/workspace","timeout_seconds":3}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()

	NewRouter(BuildInfo{Version: "test"}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("foreground response: status=%d headers=%v", response.Code, response.Header())
	}
	wantCommand := application.Execute{SandboxID: executionHandlerSandboxID, Spec: domain.ExecutionSpec{Argv: []string{"printf", "ok"}, Env: map[string]string{"A": "B"}, Cwd: "/workspace", Timeout: 3 * time.Second}}
	if !reflect.DeepEqual(service.commands, []application.Execute{wantCommand}) {
		t.Fatalf("command mapping: %+v", service.commands)
	}
	for _, event := range events {
		if !strings.Contains(response.Body.String(), "id: "+strconv.FormatUint(event.Sequence, 10)+"\n") || !strings.Contains(response.Body.String(), "event: "+string(event.Type)+"\n") {
			t.Fatalf("missing SSE event %+v in %q", event, response.Body.String())
		}
	}
	if !stream.closed {
		t.Fatal("application stream was not closed")
	}
}

func TestForegroundExecutionHandlerRejectsBeforeApplication(t *testing.T) {
	tests := []struct {
		name, path, contentType, accept, body string
		status                                int
	}{
		{name: "bad sandbox", path: "/v1/sandboxes/not-an-id/executions", contentType: "application/json", accept: "text/event-stream", body: `{"argv":["true"]}`, status: http.StatusBadRequest},
		{name: "content type", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions", contentType: "text/plain", accept: "text/event-stream", body: `{"argv":["true"]}`, status: http.StatusBadRequest},
		{name: "unknown field", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions", contentType: "application/json", accept: "text/event-stream", body: `{"argv":["true"],"secret":"x"}`, status: http.StatusBadRequest},
		{name: "trailing JSON", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions", contentType: "application/json", accept: "text/event-stream", body: `{"argv":["true"]}{}`, status: http.StatusBadRequest},
		{name: "accept", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions", contentType: "application/json", accept: "application/json", body: `{"argv":["true"]}`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Accept", test.accept)
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
			if response.Code != test.status || len(service.commands) != 0 {
				t.Fatalf("response=%d commands=%d", response.Code, len(service.commands))
			}
		})
	}
}

func TestBackgroundExecutionHandlerReturnsMinimalDescriptorAndLocation(t *testing.T) {
	service := &apiExecutionServiceFake{result: application.ExecutionResult{Descriptor: &application.ExecutionDescriptor{ID: "exec_test", State: protocol.ExecutionStateRunning}}}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"shell":"echo ok","background":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("background status: %d body=%s", response.Code, response.Body.String())
	}
	wantLocation := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test"
	if response.Header().Get("Location") != wantLocation {
		t.Fatalf("Location: %q", response.Header().Get("Location"))
	}
	encoded := response.Body.String()
	var descriptor protocol.ExecutionDescriptor
	if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&descriptor); err != nil || descriptor.ExecutionID != "exec_test" || descriptor.State != protocol.ExecutionStateRunning {
		t.Fatalf("descriptor: %+v err=%v", descriptor, err)
	}
	if len(service.commands) != 1 || !service.commands[0].Background || service.commands[0].Spec.Shell != "echo ok" {
		t.Fatalf("background command: %+v", service.commands)
	}
	for _, forbidden := range []string{"socket", "token", "pid", "pgid", "/run/minisandbox"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBackgroundExecutionHandlerMapsApplicationFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "limit", err: domain.ErrExecutionLimitReached, want: http.StatusTooManyRequests},
		{name: "not running", err: domain.ErrSandboxNotRunning, want: http.StatusConflict},
		{name: "runner", err: domain.ErrRunnerUnhealthy, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{err: test.err}
			request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["true"],"background":true}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status: got %d want %d", response.Code, test.want)
			}
		})
	}
}

func TestBackgroundExecutionResponseWriteFailureDoesNotCancelAcceptedWork(t *testing.T) {
	continued := make(chan struct{})
	service := &apiExecutionServiceFake{
		result: application.ExecutionResult{Descriptor: &application.ExecutionDescriptor{ID: "exec_write_failure", State: protocol.ExecutionStateRunning}},
		executeHook: func(_ context.Context, command application.Execute) {
			if !command.Background {
				t.Fatal("response failure fixture was not background")
			}
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(continued)
			}()
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["true"],"background":true}`))
	request.Header.Set("Content-Type", "application/json")
	writer := &apiFailingResponseWriter{header: make(http.Header)}
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(writer, request)
	if writer.status != http.StatusAccepted {
		t.Fatalf("background response status: %d", writer.status)
	}
	select {
	case <-continued:
	case <-time.After(time.Second):
		t.Fatal("accepted background work inherited response write failure")
	}
}

func TestExecutionStatusHandlerMapsBoundQuery(t *testing.T) {
	duration, truncated, exitCode := int64(4), false, 0
	terminal := protocol.ExecutionEvent{ExecutionID: "exec_test", Sequence: 2, Timestamp: time.Now().UTC(), Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: &duration, OutputTruncated: &truncated}
	service := &apiExecutionServiceFake{status: application.ExecutionStatus{Descriptor: application.ExecutionDescriptor{ID: "exec_test", State: protocol.ExecutionStateExited}, TerminalEvent: &terminal}}
	request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions/exec_test", nil)
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status response: %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(service.statusCalls, [][2]string{{executionHandlerSandboxID, "exec_test"}}) {
		t.Fatalf("status selection: %v", service.statusCalls)
	}
	var got protocol.ExecutionStatus
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil || got.ExecutionID != "exec_test" || got.TerminalEvent == nil || got.TerminalEvent.Type != protocol.EventExited {
		t.Fatalf("mapped status: %+v err=%v", got, err)
	}
}

func TestExecutionStatusHandlerRejectsIDsAndMapsErrors(t *testing.T) {
	for _, test := range []struct {
		name, path string
		err        error
		want       int
	}{
		{name: "bad sandbox", path: "/v1/sandboxes/bad/executions/exec_test", want: http.StatusBadRequest},
		{name: "bad execution", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/bad.id", want: http.StatusBadRequest},
		{name: "not found", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_missing", err: domain.ErrExecutionNotFound, want: http.StatusNotFound},
		{name: "not running", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test", err: domain.ErrSandboxNotRunning, want: http.StatusConflict},
		{name: "runner", path: "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test", err: domain.ErrRunnerUnhealthy, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{statusErr: test.err}
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.want {
				t.Fatalf("status: got %d want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCancelExecutionHandlerMapsIdempotentDisposition(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition application.CancelDisposition
		want        int
	}{
		{name: "accepted", disposition: application.CancelAccepted, want: http.StatusAccepted},
		{name: "terminal", disposition: application.CancelAlreadyTerminal, want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{cancelDisposition: test.disposition}
			path := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test"
			for range 2 {
				response := httptest.NewRecorder()
				NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, httptest.NewRequest(http.MethodDelete, path, nil))
				if response.Code != test.want {
					t.Fatalf("cancel status: got %d want %d", response.Code, test.want)
				}
			}
			if !reflect.DeepEqual(service.cancelCalls, [][2]string{{executionHandlerSandboxID, "exec_test"}, {executionHandlerSandboxID, "exec_test"}}) {
				t.Fatalf("cancel selection: %v", service.cancelCalls)
			}
		})
	}
}

func TestCancelExecutionHandlerRejectsControlsAndMapsErrors(t *testing.T) {
	base := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test"
	for _, test := range []struct {
		name, path, body string
		err              error
		want             int
	}{
		{name: "query signal", path: base + "?signal=KILL", want: http.StatusBadRequest},
		{name: "query force", path: base + "?force=true", want: http.StatusBadRequest},
		{name: "body pid", path: base, body: `{"pid":1}`, want: http.StatusBadRequest},
		{name: "unknown", path: base, err: domain.ErrExecutionNotFound, want: http.StatusNotFound},
		{name: "not running", path: base, err: domain.ErrSandboxNotRunning, want: http.StatusConflict},
		{name: "runner", path: base, err: domain.ErrRunnerUnhealthy, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &apiExecutionServiceFake{cancelErr: test.err, cancelDisposition: application.CancelAccepted}
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			response := httptest.NewRecorder()
			NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, httptest.NewRequest(http.MethodDelete, test.path, body))
			if response.Code != test.want {
				t.Fatalf("cancel status: got %d want %d", response.Code, test.want)
			}
		})
	}
}

func TestExecutionLogsHandlerMapsCursorPage(t *testing.T) {
	events := foregroundHandlerEvents()
	service := &apiExecutionServiceFake{logPage: application.ExecutionLogPage{Events: events[1:], NextCursor: 3, Complete: true}}
	path := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test/logs?cursor=1&limit=2"
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("logs status: %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(service.logCalls, []apiLogCall{{sandboxID: executionHandlerSandboxID, executionID: "exec_test", cursor: 1, limit: 2}}) {
		t.Fatalf("log call: %+v", service.logCalls)
	}
	var page protocol.ExecutionLogPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil || len(page.Events) != 2 || page.NextCursor != 3 || !page.Complete {
		t.Fatalf("log page: %+v err=%v", page, err)
	}
}

func TestExecutionLogsHandlerRejectsArbitraryQueryAndInvalidNumbers(t *testing.T) {
	base := "/v1/sandboxes/" + executionHandlerSandboxID + "/executions/exec_test/logs"
	for _, query := range []string{"?path=/run/secret", "?cursor=-1", "?cursor=01", "?cursor=1&cursor=2", "?limit=-1", "?limit=01", "?limit=1&limit=2"} {
		service := &apiExecutionServiceFake{}
		response := httptest.NewRecorder()
		NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+query, nil))
		if response.Code != http.StatusBadRequest || len(service.logCalls) != 0 {
			t.Fatalf("query %q: status=%d calls=%v", query, response.Code, service.logCalls)
		}
	}
}

func TestForegroundExecutionHandlerMapsServiceErrorBeforeSSE(t *testing.T) {
	service := &apiExecutionServiceFake{err: domain.ErrSandboxNotRunning}
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+executionHandlerSandboxID+"/executions", strings.NewReader(`{"argv":["true"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	NewRouter(BuildInfo{}, RouterDependencies{Execution: service}).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("service error response: %d %v", response.Code, response.Header())
	}
	var envelope protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Code != string(protocol.ErrorCodeSandboxNotRunning) {
		t.Fatalf("error envelope: %+v err=%v", envelope, err)
	}
}

type apiExecutionServiceFake struct {
	result            application.ExecutionResult
	err               error
	commands          []application.Execute
	status            application.ExecutionStatus
	statusErr         error
	statusCalls       [][2]string
	cancelDisposition application.CancelDisposition
	cancelErr         error
	cancelCalls       [][2]string
	logPage           application.ExecutionLogPage
	logErr            error
	logCalls          []apiLogCall
	executeHook       func(context.Context, application.Execute)
}

func (s *apiExecutionServiceFake) Status(_ context.Context, sandboxID, executionID string) (application.ExecutionStatus, error) {
	s.statusCalls = append(s.statusCalls, [2]string{sandboxID, executionID})
	return s.status, s.statusErr
}

func (s *apiExecutionServiceFake) Cancel(_ context.Context, sandboxID, executionID string) (application.CancelDisposition, error) {
	s.cancelCalls = append(s.cancelCalls, [2]string{sandboxID, executionID})
	return s.cancelDisposition, s.cancelErr
}

func (s *apiExecutionServiceFake) Logs(_ context.Context, sandboxID, executionID string, cursor uint64, limit int) (application.ExecutionLogPage, error) {
	s.logCalls = append(s.logCalls, apiLogCall{sandboxID: sandboxID, executionID: executionID, cursor: cursor, limit: limit})
	return s.logPage, s.logErr
}

type apiLogCall struct {
	sandboxID   string
	executionID string
	cursor      uint64
	limit       int
}

func (s *apiExecutionServiceFake) Execute(ctx context.Context, command application.Execute) (application.ExecutionResult, error) {
	s.commands = append(s.commands, command)
	if s.executeHook != nil {
		s.executeHook(ctx, command)
	}
	return s.result, s.err
}

type apiExecutionStreamFake struct {
	events []protocol.ExecutionEvent
	err    error
	closed bool
}

type apiFailingResponseWriter struct {
	header http.Header
	status int
}

func (w *apiFailingResponseWriter) Header() http.Header     { return w.header }
func (w *apiFailingResponseWriter) WriteHeader(status int)  { w.status = status }
func (*apiFailingResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func (s *apiExecutionStreamFake) Consume(consume func(protocol.ExecutionEvent) error) error {
	for _, event := range s.events {
		if err := consume(event); err != nil {
			return err
		}
	}
	return s.err
}

func (s *apiExecutionStreamFake) Close() error { s.closed = true; return nil }

func foregroundHandlerEvents() []protocol.ExecutionEvent {
	now := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	duration, truncated, exitCode := int64(1), false, 0
	return []protocol.ExecutionEvent{
		{ExecutionID: "exec_test", Sequence: 1, Timestamp: now, Type: protocol.EventStarted},
		{ExecutionID: "exec_test", Sequence: 2, Timestamp: now, Type: protocol.EventStdout, DataBase64: base64.StdEncoding.EncodeToString([]byte("ok"))},
		{ExecutionID: "exec_test", Sequence: 3, Timestamp: now, Type: protocol.EventExited, ExitCode: &exitCode, DurationMS: &duration, OutputTruncated: &truncated},
	}
}
