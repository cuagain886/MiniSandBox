package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

type foregroundLauncherFunc func(context.Context, ExecutionLaunchRequest) (*ExecutionHandle, error)

func (f foregroundLauncherFunc) StartForeground(ctx context.Context, request ExecutionLaunchRequest) (*ExecutionHandle, error) {
	return f(ctx, request)
}

type backgroundLauncherFunc func(context.Context, ExecutionLaunchRequest) (ExecutionDescriptor, error)

func (f backgroundLauncherFunc) StartBackground(ctx context.Context, request ExecutionLaunchRequest) (ExecutionDescriptor, error) {
	return f(ctx, request)
}

// TestForegroundCreateHandlerRequiresAuthMethodAndMediaTypes 验证鉴权外层以及 method/content-type/accept 门禁。
func TestForegroundCreateHandlerRequiresAuthMethodAndMediaTypes(t *testing.T) {
	handler := authenticatedForegroundHandler(t, foregroundSuccessLauncher(t), func(http.ResponseWriter, *http.Request, *ExecutionHandle) {})
	tests := []struct {
		name        string
		method      string
		token       bool
		contentType string
		accept      string
		want        int
	}{
		{name: "missing auth", method: http.MethodPost, contentType: "application/json", accept: "text/event-stream", want: http.StatusUnauthorized},
		{name: "method", method: http.MethodGet, token: true, contentType: "application/json", accept: "text/event-stream", want: http.StatusMethodNotAllowed},
		{name: "content type", method: http.MethodPost, token: true, contentType: "text/plain", accept: "text/event-stream", want: http.StatusUnsupportedMediaType},
		{name: "accept", method: http.MethodPost, token: true, contentType: "application/json", accept: "application/json", want: http.StatusNotAcceptable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/v1/executions", strings.NewReader(`{"argv":["true"]}`))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Accept", test.accept)
			if test.token {
				request.Header.Set("Authorization", "Bearer test-token")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status: got %d want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

// TestForegroundCreateHandlerStrictlyDecodesAndValidates 验证超限、未知字段、尾随 JSON 和语义错误不会调用 launcher。
func TestForegroundCreateHandlerStrictlyDecodesAndValidates(t *testing.T) {
	var calls atomic.Int64
	launcher := foregroundLauncherFunc(func(context.Context, ExecutionLaunchRequest) (*ExecutionHandle, error) {
		calls.Add(1)
		return nil, errors.New("unexpected")
	})
	handler := foregroundHandler(t, 64, launcher, func(http.ResponseWriter, *http.Request, *ExecutionHandle) {})
	tests := []struct {
		body string
		want int
	}{
		{body: `{`, want: http.StatusBadRequest},
		{body: `{"argv":["true"],"unknown":1}`, want: http.StatusBadRequest},
		{body: `{"argv":["true"]} {}`, want: http.StatusBadRequest},
		{body: `{"argv":["123456789012345678901234567890123456789012345678901234567890"]}`, want: http.StatusBadRequest},
		{body: `{}`, want: http.StatusUnprocessableEntity},
		{body: `{"argv":["true"],"background":true}`, want: http.StatusBadRequest},
	}
	for index, test := range tests {
		request := foregroundRequest(test.body)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("case %d: status=%d content-type=%q body=%s", index, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("launcher called %d times", calls.Load())
	}
}

// TestForegroundCreateHandlerReturnsJSONBeforeHeadersOnStartFailure 验证启动失败只写 JSON，不会再交给 SSE stream。
func TestForegroundCreateHandlerReturnsJSONBeforeHeadersOnStartFailure(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{
		{ErrExecutionLimitReached, http.StatusTooManyRequests},
		{ErrRunnerShuttingDown, http.StatusServiceUnavailable},
		{ErrInvalidCWD, http.StatusUnprocessableEntity},
		{ErrShellNotFound, http.StatusUnprocessableEntity},
		{ErrProcessStartFailed, http.StatusUnprocessableEntity},
		{errors.New("secret internal cause"), http.StatusInternalServerError},
	} {
		streamed := false
		handler := foregroundHandler(t, 1024, foregroundLauncherFunc(func(context.Context, ExecutionLaunchRequest) (*ExecutionHandle, error) {
			return nil, test.err
		}), func(http.ResponseWriter, *http.Request, *ExecutionHandle) { streamed = true })
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, foregroundRequest(`{"argv":["true"]}`))
		if response.Code != test.want || streamed || strings.Contains(response.Body.String(), "secret internal cause") || strings.Contains(response.Body.String(), "data:") {
			t.Fatalf("error %v: status=%d streamed=%v body=%s", test.err, response.Code, streamed, response.Body.String())
		}
		var envelope protocol.ErrorResponse
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Code == "" {
			t.Fatalf("error envelope: %+v err=%v", envelope, err)
		}
	}
}

// TestForegroundCreateHandlerHandsOffWithoutCommittingResponse 验证成功时传递解耦请求和 handle，handler 自身不写 header/body。
func TestForegroundCreateHandlerHandsOffWithoutCommittingResponse(t *testing.T) {
	store := testCreateHandlerStore(t)
	defer store.Close()
	var captured ExecutionLaunchRequest
	launcher := foregroundLauncherFunc(func(ctx context.Context, request ExecutionLaunchRequest) (*ExecutionHandle, error) {
		if ctx == nil {
			t.Fatal("request context missing")
		}
		captured = request
		request.Env["MUTATED"] = "launcher"
		return &ExecutionHandle{ExecutionID: "exec_handoff", Events: store}, nil
	})
	streamed := false
	handler := foregroundHandler(t, 1024, launcher, func(w http.ResponseWriter, _ *http.Request, handle *ExecutionHandle) {
		streamed = true
		if recorder := w.(*httptest.ResponseRecorder); recorder.Code != http.StatusOK || recorder.Body.Len() != 0 || recorder.Header().Get("Content-Type") != "" {
			t.Fatalf("response committed before stream: code=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
		}
		if handle.ExecutionID != "exec_handoff" || handle.Events != store {
			t.Fatalf("handle: %+v", handle)
		}
		w.WriteHeader(http.StatusOK)
	})
	body := `{"argv":["echo","ok"],"cwd":"/workspace/project","env":{"SAFE":"value"},"timeout_seconds":3}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, foregroundRequest(body))
	if !streamed || response.Code != http.StatusOK || captured.Validated.Timeout != 3*time.Second || captured.Validated.Background || captured.CWD != "/workspace/project" || captured.Env["SAFE"] != "value" {
		t.Fatalf("handoff: streamed=%v response=%d request=%+v", streamed, response.Code, captured)
	}
}

func foregroundHandler(t *testing.T, maxBytes int64, launcher ForegroundLauncher, stream ForegroundStreamFunc) http.Handler {
	t.Helper()
	validator, err := NewRequestValidator(runnerbootstrap.Limits{
		DefaultTimeoutNanoseconds: time.Second,
		MaxTimeoutNanoseconds:     time.Minute,
		MaxRequestBytes:           maxBytes,
	})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	handler, err := NewExecutionCreateHandler(ExecutionCreateHandlerConfig{
		MaxRequestBytes:    maxBytes,
		Validator:          validator,
		ForegroundLauncher: launcher,
		ForegroundStream:   stream,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler
}

func authenticatedForegroundHandler(t *testing.T, launcher ForegroundLauncher, stream ForegroundStreamFunc) http.Handler {
	t.Helper()
	handler := foregroundHandler(t, 1024, launcher, stream)
	authenticated, err := TokenAuth("test-token", handler)
	if err != nil {
		t.Fatalf("auth handler: %v", err)
	}
	return authenticated
}

func foregroundSuccessLauncher(t *testing.T) ForegroundLauncher {
	t.Helper()
	store := testCreateHandlerStore(t)
	t.Cleanup(store.Close)
	return foregroundLauncherFunc(func(context.Context, ExecutionLaunchRequest) (*ExecutionHandle, error) {
		return &ExecutionHandle{ExecutionID: "exec_success", Events: store}, nil
	})
}

func testCreateHandlerStore(t *testing.T) *EventStore {
	t.Helper()
	store, err := NewEventStore("exec_handler", fixedClock{value: time.Now().UTC()}, 1024)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}
	return store
}

func foregroundRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/executions", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "text/event-stream")
	return request
}

// TestBackgroundCreateHandlerReturns202AndImmediateDescriptor 验证后台启动接受后返回公开 descriptor，且可立即从 Manager 查询。
func TestBackgroundCreateHandlerReturns202AndImmediateDescriptor(t *testing.T) {
	execution := newPendingExecution("exec_background_202", time.Now())
	manager, err := newManager(1, creatorFunc(func() (*Execution, error) { return execution, nil }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	var captured ExecutionLaunchRequest
	background := backgroundLauncherFunc(func(ctx context.Context, request ExecutionLaunchRequest) (ExecutionDescriptor, error) {
		if ctx != serverContext {
			t.Fatal("background launcher did not receive server context")
		}
		captured = request
		created, err := manager.CreateExecution()
		if err != nil {
			return ExecutionDescriptor{}, err
		}
		if err := created.Transition(ExecutionRunning, TerminationNone, nil); err != nil {
			return ExecutionDescriptor{}, err
		}
		return created.Descriptor(), nil
	})
	handler := executionHandlerWithBackground(t, foregroundSuccessLauncher(t), background, serverContext)
	request := backgroundRequest(`{"argv":["echo","ok"],"background":true,"env":{"SAFE":"yes"}}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var descriptor protocol.ExecutionDescriptor
	if err := json.NewDecoder(response.Body).Decode(&descriptor); err != nil {
		t.Fatalf("decode descriptor: %v", err)
	}
	queried, err := manager.Descriptor(ExecutionID(descriptor.ExecutionID))
	if err != nil || descriptor.State != protocol.ExecutionStateRunning || queried.State != ExecutionRunning || captured.Env["SAFE"] != "yes" {
		t.Fatalf("descriptor=%+v queried=%+v captured=%+v err=%v", descriptor, queried, captured, err)
	}
}

// TestBackgroundCreateHandlerIgnoresRequestContextAndResponseFailure 验证请求取消及 202 写失败都不传播给后台执行。
func TestBackgroundCreateHandlerIgnoresRequestContextAndResponseFailure(t *testing.T) {
	serverContext, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	continued := make(chan struct{})
	background := backgroundLauncherFunc(func(ctx context.Context, _ ExecutionLaunchRequest) (ExecutionDescriptor, error) {
		if ctx.Err() != nil {
			t.Fatalf("server context unexpectedly cancelled: %v", ctx.Err())
		}
		go func() { close(continued) }()
		return ExecutionDescriptor{ID: "exec_response_failure", State: ExecutionRunning, CreatedAt: time.Now()}, nil
	})
	handler := executionHandlerWithBackground(t, foregroundSuccessLauncher(t), background, serverContext)
	request := backgroundRequest(`{"argv":["true"],"background":true}`)
	requestContext, cancelRequest := context.WithCancel(request.Context())
	cancelRequest()
	request = request.WithContext(requestContext)
	writer := &failingResponseWriter{header: make(http.Header)}
	handler.ServeHTTP(writer, request)
	if writer.status != http.StatusAccepted {
		t.Fatalf("status: %d", writer.status)
	}
	select {
	case <-continued:
	case <-time.After(time.Second):
		t.Fatal("background execution stopped with response failure")
	}
}

// TestBackgroundCreateHandlerMapsValidationLimitAndStartFailure 验证后台分支复用 validator 和启动错误映射。
func TestBackgroundCreateHandlerMapsValidationLimitAndStartFailure(t *testing.T) {
	serverContext := context.Background()
	var calls atomic.Int64
	background := backgroundLauncherFunc(func(context.Context, ExecutionLaunchRequest) (ExecutionDescriptor, error) {
		calls.Add(1)
		return ExecutionDescriptor{}, ErrExecutionLimitReached
	})
	handler := executionHandlerWithBackground(t, foregroundSuccessLauncher(t), background, serverContext)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, backgroundRequest(`{"background":true}`))
	if invalid.Code != http.StatusUnprocessableEntity || calls.Load() != 0 {
		t.Fatalf("validation: status=%d calls=%d", invalid.Code, calls.Load())
	}
	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, backgroundRequest(`{"argv":["true"],"background":true}`))
	if limited.Code != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("limit: status=%d calls=%d body=%s", limited.Code, calls.Load(), limited.Body.String())
	}
	failedHandler := executionHandlerWithBackground(t, foregroundSuccessLauncher(t), backgroundLauncherFunc(func(context.Context, ExecutionLaunchRequest) (ExecutionDescriptor, error) {
		return ExecutionDescriptor{}, ErrProcessStartFailed
	}), serverContext)
	failed := httptest.NewRecorder()
	failedHandler.ServeHTTP(failed, backgroundRequest(`{"argv":["missing"],"background":true}`))
	if failed.Code != http.StatusUnprocessableEntity || strings.Contains(failed.Body.String(), "missing") {
		t.Fatalf("start failure: status=%d body=%s", failed.Code, failed.Body.String())
	}
}

// TestMapExecutionDescriptorRedactsInternalFields 验证公开 descriptor 只包含 ID/state。
func TestMapExecutionDescriptorRedactsInternalFields(t *testing.T) {
	descriptor, err := MapExecutionDescriptor(ExecutionDescriptor{
		ID:                "exec_public",
		State:             ExecutionCancelled,
		CreatedAt:         time.Now(),
		TerminationReason: TerminationRunnerShutdown,
	})
	if err != nil {
		t.Fatalf("map descriptor: %v", err)
	}
	encoded, _ := json.Marshal(descriptor)
	if string(encoded) != `{"execution_id":"exec_public","state":"Cancelled"}` {
		t.Fatalf("public descriptor: %s", encoded)
	}
}

type failingResponseWriter struct {
	header http.Header
	status int
}

func (w *failingResponseWriter) Header() http.Header    { return w.header }
func (w *failingResponseWriter) WriteHeader(status int) { w.status = status }
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func executionHandlerWithBackground(
	t *testing.T,
	foreground ForegroundLauncher,
	background BackgroundLauncher,
	serverContext context.Context,
) http.Handler {
	t.Helper()
	validator, err := NewRequestValidator(runnerbootstrap.Limits{
		DefaultTimeoutNanoseconds: time.Second,
		MaxTimeoutNanoseconds:     time.Minute,
		MaxRequestBytes:           1024,
	})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	handler, err := NewExecutionCreateHandler(ExecutionCreateHandlerConfig{
		MaxRequestBytes:    1024,
		Validator:          validator,
		ForegroundLauncher: foreground,
		ForegroundStream:   func(http.ResponseWriter, *http.Request, *ExecutionHandle) {},
		ServerContext:      serverContext,
		BackgroundLauncher: background,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler
}

func backgroundRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/executions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request
}
