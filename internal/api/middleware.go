package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"minisandbox/internal/observability/logging"
)

const requestIDHeader = "X-Request-ID"

var errRequestIDGeneration = errors.New("request ID generation unavailable")

type requestIDRandom func([]byte) (int, error)

// requestIDMiddleware 复用通过安全校验的调用方 ID，否则生成加密随机 ID 并写回响应头和 context。
func requestIDMiddleware(next http.Handler) http.Handler {
	return requestIDMiddlewareWithRandom(next, rand.Read)
}

func requestIDMiddlewareWithRandom(next http.Handler, random requestIDRandom) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := logging.NewSafeID(logging.IDKindRequest, r.Header.Get(requestIDHeader))
		if err != nil {
			requestID, err = generateRequestID(random)
		}
		if err != nil {
			fallback, _ := logging.NewSafeID(logging.IDKindRequest, "request-id-generation-failed")
			w.Header().Set(requestIDHeader, fallback.String())
			r.Header.Set(requestIDHeader, fallback.String())
			r = r.WithContext(logging.WithRequestID(r.Context(), fallback))
			writeError(w, r, errRequestIDGeneration)
			return
		}
		w.Header().Set(requestIDHeader, requestID.String())
		r.Header.Set(requestIDHeader, requestID.String())
		r = r.WithContext(logging.WithRequestID(r.Context(), requestID))
		next.ServeHTTP(w, r)
	})
}

func generateRequestID(random requestIDRandom) (logging.SafeID, error) {
	if random == nil {
		return logging.SafeID{}, errRequestIDGeneration
	}
	var value [16]byte
	read, err := random(value[:])
	if err != nil || read != len(value) {
		return logging.SafeID{}, errors.Join(errRequestIDGeneration, err, io.ErrUnexpectedEOF)
	}
	return logging.NewSafeID(logging.IDKindRequest, hex.EncodeToString(value[:]))
}
