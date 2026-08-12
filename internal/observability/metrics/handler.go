package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// AuthMiddleware 是 metrics 与 diagnostics 复用的管理端鉴权包装器。
type AuthMiddleware func(http.Handler) http.Handler

// NewHandler 创建带鉴权、并发上限和写超时的 OpenMetrics handler。
func NewHandler(gatherer prometheus.Gatherer, authenticate AuthMiddleware, timeout time.Duration, maxConcurrent int) http.Handler {
	if gatherer == nil || authenticate == nil || timeout <= 0 || maxConcurrent <= 0 {
		panic("metrics handler dependencies are invalid")
	}
	base := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{EnableOpenMetrics: true, ErrorHandling: promhttp.HTTPErrorOnError})
	semaphore := make(chan struct{}, maxConcurrent)
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		default:
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		base.ServeHTTP(w, r.WithContext(ctx))
	})
	return authenticate(http.TimeoutHandler(bounded, timeout, "Service Unavailable\n"))
}
