package runner

import (
	"context"
	"net/http"
	"time"
)

// NewShutdownHandler 创建当前 runner 的固定 cancel-all 端点。
//
// handler 先永久关闭 readiness，再以固定上限终止全部 execution；即使调用方断开，关闭流程也不会回滚。
func NewShutdownHandler(manager *Manager, readiness *ServerReadiness, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if manager == nil || readiness == nil || timeout <= 0 {
			writeRunnerError(w, http.StatusInternalServerError, "RUNNER_SHUTDOWN_UNAVAILABLE", "runner shutdown is unavailable", false)
			return
		}
		readiness.StartDraining()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			writeRunnerError(w, http.StatusServiceUnavailable, "RUNNER_SHUTDOWN_TIMEOUT", "runner shutdown did not complete", true)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
