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
