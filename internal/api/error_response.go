package api

import "net/http"

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func notImplemented(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotImplemented, errorResponse{
			Code:    "not_implemented",
			Message: feature + " is not implemented in the initialization scaffold",
		})
	}
}
