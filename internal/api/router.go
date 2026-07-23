package api

import (
	"encoding/json"
	"net/http"
)

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

func NewRouter(build BuildInfo) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "sandboxd",
			"build":   build,
		})
	})
	registerLifecycleRoutes(mux)
	registerExecutionRoutes(mux)
	return requestIDMiddleware(mux)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
