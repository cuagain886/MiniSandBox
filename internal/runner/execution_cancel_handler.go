package runner

import (
	"errors"
	"net/http"

	"minisandbox/pkg/protocol"
)

// NewExecutionCancelHandler 创建幂等异步 DELETE handler；响应不等待完整进程组终止流程。
func NewExecutionCancelHandler(manager *Manager) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("execution cancel handler is not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			writeRunnerError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed", false)
			return
		}
		id, ok := executionIDFromPath(request.URL)
		if !ok {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution was not found", false)
			return
		}
		disposition, err := manager.CancelAsync(id)
		if errors.Is(err, ErrExecutionNotFound) {
			writeRunnerError(w, http.StatusNotFound, string(protocol.ErrorCodeExecutionNotFound), "execution was not found", false)
			return
		}
		if err != nil {
			writeRunnerError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "execution cancellation failed", false)
			return
		}
		if disposition == CancelAlreadyTerminal {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}), nil
}
