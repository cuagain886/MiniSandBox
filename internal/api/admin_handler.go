package api

import (
	"context"
	"net/http"

	"minisandbox/internal/application"
)

// DiagnosticsService 是 admin handler 使用的只读 snapshot 端口。
type DiagnosticsService interface {
	Snapshot(ctx context.Context) application.DiagnosticsSnapshot
}

// NewDiagnosticsHandler 返回只执行一次只读 snapshot 的 JSON handler。
func NewDiagnosticsHandler(service DiagnosticsService) http.Handler {
	if service == nil {
		panic("diagnostics service is nil")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, service.Snapshot(r.Context()))
	})
}
