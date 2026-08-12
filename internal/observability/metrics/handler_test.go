package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type failingGatherer struct{}

func (failingGatherer) Gather() ([]*dto.MetricFamily, error) {
	return nil, errors.New("collector failure")
}

// TestMetricsHandlerAuthFormatAndMethod 验证鉴权、OpenMetrics 内容和 GET-only 语义。
func TestMetricsHandlerAuthFormatAndMethod(t *testing.T) {
	registry := NewRegistry()
	counters, _ := NewExecutionCounters(registry)
	counters.ObserveExecutionRequest("foreground", "accepted")
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	handler := NewHandler(registry.Gatherer(), auth, time.Second, 1)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "minisandbox_execution_requests_total") {
		t.Fatalf("metrics: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method: %d", response.Code)
	}
}

// TestMetricsHandlerCollectorFailureAndConcurrencyBound 验证 collector failure 与并发闸门不会伪装成功。
func TestMetricsHandlerCollectorFailureAndConcurrencyBound(t *testing.T) {
	auth := func(next http.Handler) http.Handler { return next }
	response := httptest.NewRecorder()
	NewHandler(failingGatherer{}, auth, time.Second, 1).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failure: %d", response.Code)
	}
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) { entered <- struct{}{}; <-block; return nil, nil })
	handler := NewHandler(gatherer, auth, time.Second, 1)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()
	<-entered
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("concurrent: %d", second.Code)
	}
	close(block)
	wait.Wait()
}
