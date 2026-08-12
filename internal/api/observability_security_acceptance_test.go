package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minisandbox/internal/adminauth"
	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
	observabilitymetrics "minisandbox/internal/observability/metrics"
	storeport "minisandbox/internal/store"
)

type observabilityAcceptanceSource struct {
	mu          sync.Mutex
	calls       int
	records     []domain.Sandbox
	anomalies   []storeport.RuntimeAnomaly
	runtimeFail bool
}

func (s *observabilityAcceptanceSource) ListAll(context.Context) ([]domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]domain.Sandbox(nil), s.records...), nil
}
func (s *observabilityAcceptanceSource) ListActiveRuntimeAnomalies(context.Context) ([]storeport.RuntimeAnomaly, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]storeport.RuntimeAnomaly(nil), s.anomalies...), nil
}
func (s *observabilityAcceptanceSource) Diagnostics(context.Context) (application.RuntimeDiagnostics, error) {
	if s.runtimeFail {
		return application.RuntimeDiagnostics{}, context.DeadlineExceeded
	}
	return application.RuntimeDiagnostics{ManagedSandboxes: 2, OutboundSandboxes: 1, DriftedSandboxes: 1}, nil
}
func (*observabilityAcceptanceSource) RunnerDiagnostics(context.Context) (application.RunnerDiagnostics, error) {
	return application.RunnerDiagnostics{Ready: 1, Unavailable: 1}, nil
}

type observabilityRunner struct {
	source *observabilityAcceptanceSource
}

func (r observabilityRunner) Diagnostics(ctx context.Context) (application.RunnerDiagnostics, error) {
	return r.source.RunnerDiagnostics(ctx)
}
func (s *observabilityAcceptanceSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type observabilityScheduler struct{}

func (observabilityScheduler) Diagnostics() application.SchedulerDiagnostics {
	return application.SchedulerDiagnostics{QueueDepth: 2, ActiveWorkers: 1}
}

// TestObservabilitySurfacesShareSafeSemantics 验证 logs、metrics、diagnostics 与 readiness
// 四个观察面在同一服务路由中保持固定字段、低基数、鉴权、秘密隔离及 degrade/recover 语义。
func TestObservabilitySurfacesShareSafeSemantics(t *testing.T) {
	const secretSentinel = "SECRET_SENTINEL_NEVER_EXPOSE"
	now := time.Date(2028, 3, 4, 5, 6, 7, 0, time.UTC)
	source := &observabilityAcceptanceSource{
		records: []domain.Sandbox{
			{ID: secretSentinel, ObservedState: domain.StateRunning},
			{ObservedState: domain.StateFailed, Reason: domain.SandboxReasonCleanupPending},
		},
		anomalies: []storeport.RuntimeAnomaly{{RuntimeAnomalyObservation: storeport.RuntimeAnomalyObservation{
			Classification: storeport.RuntimeAnomalyUnknownSchema,
		}}},
	}
	registry := observabilitymetrics.NewRegistry()
	reliability, err := observabilitymetrics.NewReliabilityMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	executions, err := observabilitymetrics.NewExecutionCounters(registry)
	if err != nil {
		t.Fatal(err)
	}
	gauges, err := observabilitymetrics.NewSnapshotGauges(registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := gauges.SampleStore(context.Background(), source, time.Second, 100); err != nil {
		t.Fatal(err)
	}
	gauges.UpdateScheduler(2, 1)
	reliability.ObserveReconcile("cleanup", "retry_scheduled")
	reliability.ObserveRetryScheduled("cleanup", "cleanup_pending")
	reliability.ObserveOrphan("unknown_schema")
	executions.ObserveExecutionRequest("foreground", "accepted")
	executions.ObserveForegroundTerminal("exited")

	tokenText := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenPath := filepath.Join(t.TempDir(), "admin-token-SECRET-PATH")
	if err := os.WriteFile(tokenPath, []byte(tokenText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatal(err)
	}
	var authenticate func(http.Handler) http.Handler
	if token, loadErr := adminauth.LoadToken(tokenPath); loadErr == nil {
		authenticate = token.Middleware
	} else {
		if os.PathSeparator != '\\' {
			t.Fatal(loadErr)
		}
		// Windows 临时目录 ACL 不能表达 POSIX 0600；中间件仍用只在测试包内构造的
		// 等价凭据验证 HTTP 语义，Linux 文件契约由 adminauth 独立测试覆盖。
		tokenPath = filepath.Join(t.TempDir(), "unavailable-token-path")
		authenticate = acceptanceBearerMiddleware(tokenText)
	}
	diagnostics, err := application.NewDiagnosticsService(source, source, observabilityScheduler{}, time.Second, func() time.Time { return now }, observabilityRunner{source: source})
	if err != nil {
		t.Fatal(err)
	}
	metricsHandler := observabilitymetrics.NewHandler(registry.Gatherer(), authenticate, time.Second, 2)
	readiness := &Readiness{}
	readiness.SetStore(true)
	readiness.SetDocker(true)
	readiness.SetArtifact(true)
	readiness.SetRecovery(true)
	readiness.SetWorker(true)
	router := NewRouter(BuildInfo{Version: "acceptance"}, RouterDependencies{
		Readiness: readiness, Metrics: metricsHandler,
		Diagnostics: authenticate(NewDiagnosticsHandler(diagnostics)),
	})

	for _, path := range []string{"/metrics", "/v1/admin/diagnostics"} {
		response := serveObservabilityRequest(router, path, "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s: %d", path, response.Code)
		}
	}
	beforeScrape := source.callCount()
	metricResponse := serveObservabilityRequest(router, "/metrics", tokenText)
	if metricResponse.Code != http.StatusOK {
		t.Fatalf("metrics status: %d", metricResponse.Code)
	}
	metricBody := metricResponse.Body.String()
	if source.callCount() != beforeScrape {
		t.Fatalf("metrics scrape queried Store: before=%d after=%d", beforeScrape, source.callCount())
	}
	for _, forbidden := range []string{secretSentinel, tokenText, tokenPath, "minisandbox_execution_total"} {
		if strings.Contains(metricBody, forbidden) {
			t.Fatalf("metrics leaked forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"minisandbox_retry_scheduled_total", "minisandbox_active_anomalies", "minisandbox_execution_requests_total", "minisandbox_metrics_snapshot_age_seconds"} {
		if !strings.Contains(metricBody, required) {
			t.Fatalf("metrics missing %s", required)
		}
	}

	diagnosticsResponse := serveObservabilityRequest(router, "/v1/admin/diagnostics", tokenText)
	if diagnosticsResponse.Code != http.StatusOK {
		t.Fatalf("diagnostics status: %d", diagnosticsResponse.Code)
	}
	if strings.Contains(diagnosticsResponse.Body.String(), secretSentinel) || strings.Contains(diagnosticsResponse.Body.String(), tokenPath) {
		t.Fatal("diagnostics leaked identity or sensitive path")
	}
	var snapshot application.DiagnosticsSnapshot
	if err := json.Unmarshal(diagnosticsResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Store.Status != "available" || snapshot.Runtime.Status != "available" || snapshot.Runner.Status != "available" ||
		snapshot.Anomalies.Classifications[string(storeport.RuntimeAnomalyUnknownSchema)] != 1 {
		t.Fatalf("diagnostics snapshot: %#v", snapshot)
	}

	if response := serveObservabilityRequest(router, "/readyz", ""); response.Code != http.StatusOK {
		t.Fatalf("ready status: %d", response.Code)
	}
	readiness.SetDocker(false)
	degraded := serveObservabilityRequest(router, "/readyz", "")
	if degraded.Code != http.StatusServiceUnavailable || strings.Contains(degraded.Body.String(), tokenPath) {
		t.Fatalf("degraded readiness: %d %s", degraded.Code, degraded.Body.String())
	}
	readiness.SetDocker(true)
	if response := serveObservabilityRequest(router, "/readyz", ""); response.Code != http.StatusOK {
		t.Fatalf("recovered ready status: %d", response.Code)
	}

	// 安全日志端口只接收 allowlist 值；原始错误、token 和路径无法构造成属性。
	var logs bytes.Buffer
	logger, err := logging.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	message, _ := logging.NewSafeValue("reconcile.result")
	operation, _ := logging.NewSafeValue("cleanup")
	result, _ := logging.NewSafeValue("retry_scheduled")
	operationAttr, _ := logging.ValueAttr(logging.FieldOperation, operation)
	resultAttr, _ := logging.ValueAttr(logging.FieldResult, result)
	logger.Log(context.Background(), slog.LevelInfo, message, operationAttr, resultAttr)
	if _, err := logging.NewSafeValue(secretSentinel + " " + tokenText + " " + tokenPath); err == nil {
		t.Fatal("unsafe log value accepted")
	}
	if strings.Contains(logs.String(), secretSentinel) || strings.Contains(logs.String(), tokenText) || strings.Contains(logs.String(), tokenPath) {
		t.Fatal("structured log leaked sentinel")
	}

	disabled := NewRouter(BuildInfo{Version: "acceptance"})
	for _, path := range []string{"/metrics", "/v1/admin/diagnostics"} {
		if response := serveObservabilityRequest(disabled, path, tokenText); response.Code != http.StatusNotFound {
			t.Fatalf("disabled route %s: %d", path, response.Code)
		}
	}
}

func serveObservabilityRequest(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func acceptanceBearerMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
