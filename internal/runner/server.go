package runner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

func NewServer(version, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "runnerd",
			"version": version,
		})
	})
	mux.HandleFunc("POST /v1/executions", notImplemented)
	mux.HandleFunc("DELETE /v1/executions/{execution_id}", notImplemented)
	return TokenAuth(token, mux)
}

func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()
			_ = server.Shutdown(shutdownContext)
		case <-stopped:
		}
	}()

	err := server.Serve(listener)
	close(stopped)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    "not_implemented",
		"message": "runner execution is not implemented in the initialization scaffold",
	})
}
