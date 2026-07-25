package api

import (
	"net/http"

	"minisandbox/pkg/protocol"
)

func notImplemented(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotImplemented, protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:      "NOT_IMPLEMENTED",
				Message:   feature + " is not implemented in the initialization scaffold",
				RequestID: w.Header().Get(requestIDHeader),
				Retryable: false,
			},
		})
	}
}
