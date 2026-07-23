package api

import "net/http"

func registerExecutionRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"POST /v1/sandboxes/{sandbox_id}/executions",
		notImplemented("command execution"),
	)
	mux.HandleFunc(
		"DELETE /v1/sandboxes/{sandbox_id}/executions/{execution_id}",
		notImplemented("execution cancellation"),
	)
}
