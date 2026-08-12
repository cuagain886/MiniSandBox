package security

import (
	"strings"
	"testing"
)

var testSentinels = map[string]string{
	"admin_token":     "admin-token-sentinel-A7m9Q2",
	"idempotency_key": "idempotency-sentinel-K4p8Z1",
	"environment":     "MINISANDBOX_SECRET=env-sentinel-V6n3",
	"command":         "printf command-sentinel-X8q2",
	"docker_host":     "tcp://docker-host-sentinel.internal:2376",
	"host_path":       "/srv/minisandbox/path-sentinel-R5t1",
	"raw_error":       "raw-error-sentinel-B9w4",
}

// TestRedactionScannerRejectsEverySentinelOnEverySurface 验证四类观察面均能检测每种禁止值。
func TestRedactionScannerRejectsEverySentinelOnEverySurface(t *testing.T) {
	scanner := newRedactionScanner(testSentinels)
	surfaces := []string{"logs", "errors", "metrics_drafts", "diagnostics_fixtures"}
	for _, surface := range surfaces {
		for kind, value := range testSentinels {
			t.Run(surface+"/"+kind, func(t *testing.T) {
				err := scanner.scan(surface, []byte("prefix "+value+" suffix"))
				if err == nil {
					t.Fatal("expected sentinel detection")
				}
				if strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "sha256=") {
					t.Fatalf("unsafe detection error: %v", err)
				}
			})
		}
	}
}

// TestRedactionScannerAllowsSafeOperationalFields 验证安全 ID、固定错误码和低基数标签仍可观察。
func TestRedactionScannerAllowsSafeOperationalFields(t *testing.T) {
	scanner := newRedactionScanner(testSentinels)
	fixtures := map[string]string{
		"logs":                 `{"request_id":"request-safe-1","sandbox_id":"sandbox-safe-1","error_code":"CAS_CONFLICT"}`,
		"errors":               `{"code":"INTERNAL_ERROR","request_id":"request-safe-1"}`,
		"metrics_drafts":       `minisandbox_reconcile_total{operation="recover",result="retry_scheduled"} 1`,
		"diagnostics_fixtures": `{"status":"degraded","classification":"unknown_schema"}`,
	}
	for surface, fixture := range fixtures {
		if err := scanner.scan(surface, []byte(fixture)); err != nil {
			t.Fatalf("safe fixture rejected for %s: %v", surface, err)
		}
	}
}

// TestRedactionScannerDoesNotTreatEmptySentinelAsLeak 验证配置缺项不会匹配所有观察内容。
func TestRedactionScannerDoesNotTreatEmptySentinelAsLeak(t *testing.T) {
	scanner := newRedactionScanner(map[string]string{"empty": "", "token": "known-sentinel"})
	if err := scanner.scan("logs", []byte("safe operational output")); err != nil {
		t.Fatal(err)
	}
}
