package protocol

import (
	"encoding/json"
	"testing"
)

// TestAnomalyDiagnosticsPresenceSemantics 验证 available 零值与 unavailable 省略字段不会混淆。
func TestAnomalyDiagnosticsPresenceSemantics(t *testing.T) {
	zero := uint64(0)
	empty := []DiagnosticsAnomalyClassification{}
	available, err := json.Marshal(AnomalyDiagnostics{
		Status:          DiagnosticsSectionAvailable,
		ActiveCount:     &zero,
		Classifications: &empty,
	})
	if err != nil {
		t.Fatalf("marshal available anomaly diagnostics: %v", err)
	}
	if got, want := string(available), `{"status":"available","active_count":0,"classifications":[]}`; got != want {
		t.Fatalf("available anomaly diagnostics: got %s, want %s", got, want)
	}

	code := DiagnosticsUnavailableTimeout
	unavailable, err := json.Marshal(AnomalyDiagnostics{
		Status:          DiagnosticsSectionUnavailable,
		UnavailableCode: &code,
	})
	if err != nil {
		t.Fatalf("marshal unavailable anomaly diagnostics: %v", err)
	}
	if got, want := string(unavailable), `{"status":"unavailable","unavailable_code":"timeout"}`; got != want {
		t.Fatalf("unavailable anomaly diagnostics: got %s, want %s", got, want)
	}
}
