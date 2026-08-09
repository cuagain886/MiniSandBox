package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

const maxExecutionRequestBodyBytes int64 = 1 << 20

// ExecutionService 定义公共 execution handler 可调用的应用层用例。
type ExecutionService interface {
	// Execute 在 application 完成 sandbox admission 后返回前台 stream 或后台 descriptor。
	Execute(context.Context, application.Execute) (application.ExecutionResult, error)
	// Status 查询指定 sandbox 内的 execution，不允许 execution ID 选择 runner。
	Status(context.Context, string, string) (application.ExecutionStatus, error)
	// Cancel 幂等取消指定 sandbox 内的 execution。
	Cancel(context.Context, string, string) (application.CancelDisposition, error)
	// Logs 读取指定 sandbox 内 execution 的 cursor 日志页。
	Logs(context.Context, string, string, uint64, int) (application.ExecutionLogPage, error)
}

func registerExecutionRoutes(mux *http.ServeMux, service ExecutionService) {
	if service == nil {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/executions", notImplemented("command execution"))
		mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/executions/{execution_id}", notImplemented("execution status"))
		mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/executions/{execution_id}/logs", notImplemented("execution logs"))
	} else {
		mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/executions", executeSandboxHandler(service))
		mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/executions/{execution_id}", executionStatusHandler(service))
		mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}/executions/{execution_id}/logs", executionLogsHandler(service))
	}
	if service == nil {
		mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}/executions/{execution_id}", notImplemented("execution cancellation"))
	} else {
		mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}/executions/{execution_id}", cancelExecutionHandler(service))
	}
}

func executionLogsHandler(service ExecutionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID, executionID := r.PathValue("sandbox_id"), r.PathValue("execution_id")
		if !validSandboxID(sandboxID) || !validExecutionID(executionID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		cursor, limit, err := parseExecutionLogQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, r, err)
			return
		}
		page, err := service.Logs(r.Context(), sandboxID, executionID, cursor, limit)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, protocol.ExecutionLogPage{Events: append([]protocol.ExecutionEvent(nil), page.Events...), NextCursor: page.NextCursor, Complete: page.Complete})
	}
}

func parseExecutionLogQuery(raw string) (uint64, int, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, 0, domain.ErrInvalidExecutionRequest
	}
	for key := range values {
		if key != "cursor" && key != "limit" {
			return 0, 0, domain.ErrInvalidExecutionRequest
		}
	}
	cursor, err := parseCanonicalUint(values, "cursor")
	if err != nil {
		return 0, 0, err
	}
	limit64, err := parseCanonicalUint(values, "limit")
	if err != nil || limit64 > uint64(^uint(0)>>1) {
		return 0, 0, domain.ErrInvalidExecutionRequest
	}
	return cursor, int(limit64), nil
}

func parseCanonicalUint(values url.Values, key string) (uint64, error) {
	items, exists := values[key]
	if !exists {
		return 0, nil
	}
	if len(items) != 1 || items[0] == "" {
		return 0, domain.ErrInvalidExecutionRequest
	}
	value, err := strconv.ParseUint(items[0], 10, 64)
	if err != nil || strconv.FormatUint(value, 10) != items[0] {
		return 0, domain.ErrInvalidExecutionRequest
	}
	return value, nil
}

func cancelExecutionHandler(service ExecutionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID, executionID := r.PathValue("sandbox_id"), r.PathValue("execution_id")
		if !validSandboxID(sandboxID) || !validExecutionID(executionID) || r.URL.RawQuery != "" || requestHasBody(r) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		disposition, err := service.Cancel(r.Context(), sandboxID, executionID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		switch disposition {
		case application.CancelAccepted:
			w.WriteHeader(http.StatusAccepted)
		case application.CancelAlreadyTerminal:
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, r, domain.ErrRunnerUnhealthy)
		}
	}
}

func requestHasBody(r *http.Request) bool {
	if r == nil || r.Body == nil {
		return false
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		return true
	}
	var value [1]byte
	count, err := r.Body.Read(value[:])
	return count != 0 || err != io.EOF
}

func executionStatusHandler(service ExecutionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID, executionID := r.PathValue("sandbox_id"), r.PathValue("execution_id")
		if !validSandboxID(sandboxID) || !validExecutionID(executionID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		status, err := service.Status(r.Context(), sandboxID, executionID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		mapped, err := mapExecutionStatus(status)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mapped)
	}
}

func mapExecutionStatus(status application.ExecutionStatus) (protocol.ExecutionStatus, error) {
	descriptor, err := mapExecutionDescriptor(status.Descriptor)
	if err != nil {
		return protocol.ExecutionStatus{}, err
	}
	result := protocol.ExecutionStatus{ExecutionID: descriptor.ExecutionID, State: descriptor.State}
	isTerminal := terminalPublicExecutionState(descriptor.State)
	if isTerminal != (status.TerminalEvent != nil) {
		return protocol.ExecutionStatus{}, domain.ErrRunnerUnhealthy
	}
	if status.TerminalEvent != nil {
		if status.TerminalEvent.ExecutionID != descriptor.ExecutionID || !status.TerminalEvent.Terminal() || status.TerminalEvent.Validate() != nil {
			return protocol.ExecutionStatus{}, domain.ErrRunnerUnhealthy
		}
		if !terminalEventMatchesState(status.TerminalEvent.Type, descriptor.State) {
			return protocol.ExecutionStatus{}, domain.ErrRunnerUnhealthy
		}
		terminal := *status.TerminalEvent
		result.TerminalEvent = &terminal
	}
	return result, nil
}

func terminalPublicExecutionState(state protocol.ExecutionState) bool {
	switch state {
	case protocol.ExecutionStateExited, protocol.ExecutionStateFailed, protocol.ExecutionStateCancelled, protocol.ExecutionStateTimedOut:
		return true
	default:
		return false
	}
}

func terminalEventMatchesState(eventType protocol.EventType, state protocol.ExecutionState) bool {
	return state == protocol.ExecutionStateExited && eventType == protocol.EventExited ||
		state == protocol.ExecutionStateFailed && eventType == protocol.EventFailed ||
		state == protocol.ExecutionStateCancelled && eventType == protocol.EventCancelled ||
		state == protocol.ExecutionStateTimedOut && eventType == protocol.EventTimedOut
}

func validExecutionID(id string) bool {
	if !strings.HasPrefix(id, "exec_") || len(id) <= len("exec_") || len(id) > 128 {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func executeSandboxHandler(service ExecutionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := r.PathValue("sandbox_id")
		if !validSandboxID(sandboxID) {
			writeError(w, r, domain.ErrInvalid)
			return
		}
		request, err := decodeExecutionRequest(w, r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if request.Background {
			handleBackgroundExecution(w, r, service, sandboxID, request)
			return
		}
		if !acceptsExecutionMediaType(r.Header.Values("Accept"), "text/event-stream") {
			writeError(w, r, domain.ErrInvalidExecutionRequest)
			return
		}
		result, err := service.Execute(r.Context(), mapExecutionCommand(sandboxID, request))
		if err != nil {
			writeError(w, r, err)
			return
		}
		if result.Stream == nil || result.Descriptor != nil {
			writeError(w, r, domain.ErrRunnerUnhealthy)
			return
		}
		defer result.Stream.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		_ = result.Stream.Consume(func(event protocol.ExecutionEvent) error {
			if event.Validate() != nil {
				return errors.New("runner returned an invalid execution event")
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			frame := "id: " + strconv.FormatUint(event.Sequence, 10) + "\n" +
				"event: " + string(event.Type) + "\n" +
				"data: " + string(encoded) + "\n\n"
			if _, err := io.WriteString(w, frame); err != nil {
				return err
			}
			return controller.Flush()
		})
	}
}

func handleBackgroundExecution(w http.ResponseWriter, r *http.Request, service ExecutionService, sandboxID string, request protocol.ExecuteRequest) {
	if values := r.Header.Values("Accept"); len(values) > 0 && !acceptsExecutionMediaType(values, "application/json") {
		writeError(w, r, domain.ErrInvalidExecutionRequest)
		return
	}
	result, err := service.Execute(r.Context(), mapExecutionCommand(sandboxID, request))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if result.Stream != nil || result.Descriptor == nil {
		writeError(w, r, domain.ErrRunnerUnhealthy)
		return
	}
	descriptor, err := mapExecutionDescriptor(*result.Descriptor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions/"+url.PathEscape(descriptor.ExecutionID))
	writeJSON(w, http.StatusAccepted, descriptor)
}

func mapExecutionDescriptor(descriptor application.ExecutionDescriptor) (protocol.ExecutionDescriptor, error) {
	if descriptor.ID == "" || !validPublicExecutionState(descriptor.State) {
		return protocol.ExecutionDescriptor{}, domain.ErrRunnerUnhealthy
	}
	return protocol.ExecutionDescriptor{ExecutionID: descriptor.ID, State: descriptor.State}, nil
}

func validPublicExecutionState(state protocol.ExecutionState) bool {
	switch state {
	case protocol.ExecutionStatePending, protocol.ExecutionStateRunning, protocol.ExecutionStateExited, protocol.ExecutionStateFailed, protocol.ExecutionStateCancelled, protocol.ExecutionStateTimedOut:
		return true
	default:
		return false
	}
}

func decodeExecutionRequest(w http.ResponseWriter, r *http.Request) (protocol.ExecuteRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	body := http.MaxBytesReader(w, r.Body, maxExecutionRequestBodyBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request protocol.ExecuteRequest
	if err := decoder.Decode(&request); err != nil {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > int64((time.Duration(1<<63-1))/time.Second) {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	if !mapExecutionSpec(request).Valid() {
		return protocol.ExecuteRequest{}, domain.ErrInvalidExecutionRequest
	}
	return request, nil
}

func mapExecutionCommand(sandboxID string, request protocol.ExecuteRequest) application.Execute {
	return application.Execute{SandboxID: sandboxID, Spec: mapExecutionSpec(request), Background: request.Background}
}

func mapExecutionSpec(request protocol.ExecuteRequest) domain.ExecutionSpec {
	environment := make(map[string]string, len(request.Env))
	for key, value := range request.Env {
		environment[key] = value
	}
	if request.Env == nil {
		environment = nil
	}
	return domain.ExecutionSpec{Argv: append([]string(nil), request.Argv...), Shell: request.Shell, Env: environment, Cwd: request.Cwd, Timeout: time.Duration(request.TimeoutSeconds) * time.Second}
}

func acceptsExecutionMediaType(values []string, want string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err == nil && parameters["q"] != "0" && (strings.EqualFold(mediaType, want) || mediaType == "*/*") {
				return true
			}
		}
	}
	return false
}
