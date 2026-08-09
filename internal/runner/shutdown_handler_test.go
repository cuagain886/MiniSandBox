package runner

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestShutdownHandlerClosesAdmission 验证 cancel-all 返回后当前 runner 永久拒绝新任务。
func TestShutdownHandlerClosesAdmission(t *testing.T) {
	manager, err := NewManager(1)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	readiness := NewServerReadiness()
	if err := readiness.MarkReady(); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	response := httptest.NewRecorder()
	NewShutdownHandler(manager, readiness, time.Second).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/shutdown", nil))
	if response.Code != http.StatusNoContent || !readiness.draining.Load() {
		t.Fatalf("shutdown result: status=%d draining=%v", response.Code, readiness.draining.Load())
	}
	if _, err := manager.CreateExecution(); err != ErrRunnerShuttingDown {
		t.Fatalf("create after shutdown: %v", err)
	}
}
