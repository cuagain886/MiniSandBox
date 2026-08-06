package runner

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"minisandbox/pkg/protocol"
)

const executionStatusPathPrefix = "/v1/executions/"

// NewExecutionStatusHandler 创建只读状态查询 handler，不等待状态变化也不返回事件历史。
func NewExecutionStatusHandler(manager *Manager) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("execution status handler is not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRunnerError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed", false)
			return
		}
		id, ok := executionIDFromPath(request.URL)
		if !ok {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution was not found", false)
			return
		}
		snapshot, err := manager.StatusSnapshot(id)
		if errors.Is(err, ErrExecutionNotFound) {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution was not found", false)
			return
		}
		if err != nil {
			writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution status is unavailable", false)
			return
		}
		status, err := MapExecutionStatus(snapshot)
		if err != nil {
			writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution status is unavailable", false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}), nil
}

// MapExecutionStatus 显式映射内部 snapshot，并校验终态状态与 terminal event 类型一致。
func MapExecutionStatus(snapshot ExecutionStatusSnapshot) (protocol.ExecutionStatus, error) {
	descriptor, err := MapExecutionDescriptor(snapshot.Descriptor)
	if err != nil {
		return protocol.ExecutionStatus{}, err
	}
	result := protocol.ExecutionStatus{ExecutionID: descriptor.ExecutionID, State: descriptor.State}
	if !terminalExecutionState(snapshot.Descriptor.State) {
		if snapshot.TerminalEvent != nil {
			return protocol.ExecutionStatus{}, errors.New("non-terminal execution has terminal event")
		}
		return result, nil
	}
	if snapshot.TerminalEvent == nil || snapshot.TerminalEvent.ExecutionID != descriptor.ExecutionID || !snapshot.TerminalEvent.Terminal() || snapshot.TerminalEvent.Validate() != nil {
		return protocol.ExecutionStatus{}, errors.New("terminal execution metadata is missing")
	}
	if !terminalStateMatchesEvent(snapshot.Descriptor.State, snapshot.TerminalEvent.Type) {
		return protocol.ExecutionStatus{}, errors.New("terminal execution metadata does not match state")
	}
	terminal := cloneExecutionEvent(*snapshot.TerminalEvent)
	result.TerminalEvent = &terminal
	return result, nil
}

func terminalStateMatchesEvent(state ExecutionState, eventType protocol.EventType) bool {
	switch state {
	case ExecutionExited:
		return eventType == protocol.EventExited
	case ExecutionFailed:
		return eventType == protocol.EventFailed
	case ExecutionCancelled:
		return eventType == protocol.EventCancelled
	case ExecutionTimedOut:
		return eventType == protocol.EventTimedOut
	default:
		return false
	}
}

func executionIDFromPath(value *url.URL) (ExecutionID, bool) {
	if value == nil || !strings.HasPrefix(value.EscapedPath(), executionStatusPathPrefix) {
		return "", false
	}
	escaped := strings.TrimPrefix(value.EscapedPath(), executionStatusPathPrefix)
	if escaped == "" || strings.ContainsAny(escaped, "/\\") {
		return "", false
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || decoded == "" || strings.ContainsAny(decoded, "/\\") {
		return "", false
	}
	for _, character := range decoded {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return ExecutionID(decoded), true
}
