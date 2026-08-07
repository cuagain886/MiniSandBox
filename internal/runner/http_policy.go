package runner

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"minisandbox/pkg/protocol"
)

const (
	runnerMaxHeaderBytes = 32 << 10
	runnerMaxPathBytes   = 512
	runnerReadTimeout    = 30 * time.Second
	runnerIdleTimeout    = 30 * time.Second
	runnerHeaderTimeout  = 5 * time.Second
)

const runnerRequestIDHeader = "X-Request-ID"

var runnerRequestSequence atomic.Uint64

// RunnerRequestPolicy 为每个请求生成内部 request ID，并在路由前限制 header 与 path 字节数。
// 它不记录 header/body 内容，避免 token、命令和环境变量进入诊断数据。
func RunnerRequestPolicy(next http.Handler) (http.Handler, error) {
	if next == nil {
		return nil, http.ErrServerClosed
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestID := "req_" + strconv.FormatUint(runnerRequestSequence.Add(1), 36)
		w.Header().Set(runnerRequestIDHeader, requestID)
		if requestHeaderBytes(request.Header) > runnerMaxHeaderBytes {
			writeRunnerError(w, http.StatusRequestHeaderFieldsTooLarge, "REQUEST_HEADERS_TOO_LARGE", "request headers exceed the runner limit", false)
			return
		}
		if len(request.URL.EscapedPath()) > runnerMaxPathBytes {
			writeRunnerError(w, http.StatusRequestURITooLong, "REQUEST_PATH_TOO_LONG", "request path exceeds the runner limit", false)
			return
		}
		next.ServeHTTP(w, request)
	}), nil
}

func newRunnerHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: runnerHeaderTimeout,
		ReadTimeout:       runnerReadTimeout,
		IdleTimeout:       runnerIdleTimeout,
		MaxHeaderBytes:    runnerMaxHeaderBytes,
	}
}

func requestHeaderBytes(header http.Header) int {
	total := 0
	for name, values := range header {
		total += len(name)
		for _, value := range values {
			total += len(value)
		}
	}
	return total
}

func writeRunnerError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	requestID := w.Header().Get(runnerRequestIDHeader)
	if requestID == "" {
		requestID = "req_" + strconv.FormatUint(runnerRequestSequence.Add(1), 36)
		w.Header().Set(runnerRequestIDHeader, requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: protocol.ErrorDetail{
		Code:      code,
		Message:   message,
		RequestID: requestID,
		Retryable: retryable,
	}})
}
