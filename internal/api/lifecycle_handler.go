package api

import "net/http"

func registerLifecycleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/sandboxes", notImplemented("sandbox creation"))
	mux.HandleFunc("GET /v1/sandboxes/{sandbox_id}", notImplemented("sandbox lookup"))
	mux.HandleFunc("DELETE /v1/sandboxes/{sandbox_id}", notImplemented("sandbox deletion"))
	mux.HandleFunc("POST /v1/sandboxes/{sandbox_id}/renew", notImplemented("sandbox renewal"))
}
