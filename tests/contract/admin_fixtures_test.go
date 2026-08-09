package contract_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// adminDisabledFixture 固定 admin 默认关闭时不注册路由、不读取 token file 的外部语义。
type adminDisabledFixture struct {
	RouteRegistered bool `json:"route_registered"`
	TokenFileRead   bool `json:"token_file_read"`
	HTTPStatus      int  `json:"http_status"`
}

// TestAdminDiagnosticsFixtures 校验完整、缺失 section、not found 和 disabled fixtures。
func TestAdminDiagnosticsFixtures(t *testing.T) {
	t.Run("fixture set", func(t *testing.T) {
		entries, err := os.ReadDir(adminFixtureDir(t))
		if err != nil {
			t.Fatalf("read admin fixture directory: %v", err)
		}
		actual := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				actual = append(actual, entry.Name())
			}
		}
		expected := []string{
			"diagnostics-disabled-contract.json",
			"diagnostics-full.json",
			"diagnostics-missing-runner-section.json",
			"diagnostics-not-found.json",
			"diagnostics-partial-unavailable.json",
		}
		slices.Sort(actual)
		if !slices.Equal(actual, expected) {
			t.Fatalf("unexpected admin fixture set: got %v, want %v", actual, expected)
		}
	})

	t.Run("full", func(t *testing.T) {
		diagnostics := decodeAdminFixture[protocol.SandboxDiagnostics](t, "diagnostics-full.json")
		if diagnostics.SandboxID != "sbx-diagnostics-01" || !diagnosticsSectionsComplete(diagnostics) {
			t.Fatalf("incomplete full diagnostics: %#v", diagnostics)
		}
		if diagnostics.Store.Status != protocol.DiagnosticsSectionAvailable ||
			diagnostics.Store.DesiredState == nil ||
			*diagnostics.Store.DesiredState != protocol.DiagnosticsDesiredRunning ||
			diagnostics.Store.ObservedState == nil ||
			*diagnostics.Store.ObservedState != protocol.SandboxStateRunning ||
			diagnostics.Store.Reason == nil ||
			*diagnostics.Store.Reason != protocol.SandboxReasonRunnerHealthDegraded {
			t.Fatalf("unexpected store diagnostics: %#v", diagnostics.Store)
		}
		if diagnostics.Runtime.SafeSpecHash == nil ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(*diagnostics.Runtime.SafeSpecHash) ||
			diagnostics.Runtime.SecurityProfile == nil ||
			*diagnostics.Runtime.SecurityProfile != protocol.DiagnosticsMatch {
			t.Fatalf("unexpected runtime diagnostics: %#v", diagnostics.Runtime)
		}
		if diagnostics.Runner.Health == nil ||
			*diagnostics.Runner.Health != protocol.DiagnosticsRunnerDegraded ||
			diagnostics.Reconcile.LastCode == nil ||
			*diagnostics.Reconcile.LastCode != protocol.DiagnosticsReconcileRetryScheduled {
			t.Fatalf("unexpected runner/reconcile diagnostics: %#v %#v", diagnostics.Runner, diagnostics.Reconcile)
		}
		if diagnostics.Anomaly.ActiveCount == nil || *diagnostics.Anomaly.ActiveCount != 2 ||
			diagnostics.Anomaly.Classifications == nil ||
			!slices.Equal(*diagnostics.Anomaly.Classifications, []protocol.DiagnosticsAnomalyClassification{
				protocol.DiagnosticsAnomalyNetworkNamespaceMismatch,
				protocol.DiagnosticsAnomalyDuplicateResource,
			}) {
			t.Fatalf("unexpected anomaly diagnostics: %#v", diagnostics.Anomaly)
		}
		assertDiagnosticsTimesUTC(t, diagnostics)
	})

	t.Run("missing section", func(t *testing.T) {
		diagnostics := decodeAdminFixture[protocol.SandboxDiagnostics](
			t,
			"diagnostics-missing-runner-section.json",
		)
		if diagnosticsSectionsComplete(diagnostics) {
			t.Fatal("fixture missing runner section was accepted as complete")
		}
		if diagnostics.Runner.Status != "" {
			t.Fatalf("missing runner section did not remain absent: %#v", diagnostics.Runner)
		}
		if diagnostics.Runtime.Status != protocol.DiagnosticsSectionUnavailable ||
			diagnostics.Runtime.UnavailableCode == nil ||
			*diagnostics.Runtime.UnavailableCode != protocol.DiagnosticsUnavailableTimeout {
			t.Fatalf("typed unavailable runtime section was not preserved: %#v", diagnostics.Runtime)
		}
	})

	t.Run("partial unavailable", func(t *testing.T) {
		diagnostics := decodeAdminFixture[protocol.SandboxDiagnostics](
			t,
			"diagnostics-partial-unavailable.json",
		)
		if !diagnosticsSectionsComplete(diagnostics) {
			t.Fatalf("partial diagnostics omitted a required section: %#v", diagnostics)
		}
		if diagnostics.Runtime.Status != protocol.DiagnosticsSectionUnavailable ||
			diagnostics.Runtime.UnavailableCode == nil ||
			*diagnostics.Runtime.UnavailableCode != protocol.DiagnosticsUnavailableTimeout ||
			diagnostics.Runner.Status != protocol.DiagnosticsSectionUnavailable ||
			diagnostics.Runner.UnavailableCode == nil ||
			*diagnostics.Runner.UnavailableCode != protocol.DiagnosticsUnavailableDependency {
			t.Fatalf("partial diagnostics lost typed unavailable sections: %#v", diagnostics)
		}
		if diagnostics.Anomaly.ActiveCount == nil || *diagnostics.Anomaly.ActiveCount != 0 ||
			diagnostics.Anomaly.Classifications == nil || len(*diagnostics.Anomaly.Classifications) != 0 {
			t.Fatalf("zero anomaly summary was not explicit: %#v", diagnostics.Anomaly)
		}
	})

	t.Run("sandbox not found", func(t *testing.T) {
		response := decodeAdminFixture[errorFixture](t, "diagnostics-not-found.json")
		assertErrorFixture(t, response, "SANDBOX_NOT_FOUND", false)
	})

	t.Run("admin disabled", func(t *testing.T) {
		contract := decodeAdminFixture[adminDisabledFixture](t, "diagnostics-disabled-contract.json")
		if contract.RouteRegistered || contract.TokenFileRead || contract.HTTPStatus != 404 {
			t.Fatalf("unexpected disabled contract: %#v", contract)
		}
	})
}

// TestAdminDiagnosticsFixtureFieldAllowlist 固定完整快照每一层允许出现的 JSON 字段。
func TestAdminDiagnosticsFixtureFieldAllowlist(t *testing.T) {
	root := readAdminFixtureObject(t, "diagnostics-full.json")
	assertAdminFixtureKeys(t, root, []string{
		"anomaly",
		"generated_at",
		"reconcile",
		"runner",
		"runtime",
		"sandbox_id",
		"store",
	})
	assertAdminFixtureKeys(t, decodeAdminFixtureObject(t, root["store"]), []string{
		"desired_state",
		"expires_at",
		"health_failure_count",
		"last_reconcile_at",
		"next_reconcile_at",
		"observed_state",
		"origin",
		"reason",
		"retry_attempt",
		"revision",
		"status",
	})
	assertAdminFixtureKeys(t, decodeAdminFixtureObject(t, root["runtime"]), []string{
		"egress_sidecar",
		"main_container",
		"runtime_directory",
		"safe_spec_hash",
		"security_profile",
		"spec_hash_match",
		"status",
		"workspace_volume",
	})
	assertAdminFixtureKeys(t, decodeAdminFixtureObject(t, root["runner"]), []string{
		"health",
		"last_checked_at",
		"status",
	})
	assertAdminFixtureKeys(t, decodeAdminFixtureObject(t, root["reconcile"]), []string{
		"last_code",
		"last_finished_at",
		"status",
	})
	assertAdminFixtureKeys(t, decodeAdminFixtureObject(t, root["anomaly"]), []string{
		"active_count",
		"classifications",
		"last_observed_at",
		"status",
	})

	allKeys := make([]string, 0)
	collectAdminFixtureKeys(root, &allKeys)
	for _, key := range allKeys {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{
			"raw",
			"log",
			"inspect",
			"command",
			"output",
			"env",
			"token",
			"authorization",
			"host_path",
			"socket",
			"data_dir",
			"stack",
			"runtime_id",
			"container_id",
		} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("diagnostics allowlist contains forbidden field %q", key)
			}
		}
	}
}

func diagnosticsSectionsComplete(diagnostics protocol.SandboxDiagnostics) bool {
	return diagnostics.Store.Status != "" &&
		diagnostics.Runtime.Status != "" &&
		diagnostics.Runner.Status != "" &&
		diagnostics.Reconcile.Status != "" &&
		diagnostics.Anomaly.Status != ""
}

func assertDiagnosticsTimesUTC(t *testing.T, diagnostics protocol.SandboxDiagnostics) {
	t.Helper()
	times := map[string]*time.Time{
		"generated_at":      &diagnostics.GeneratedAt,
		"expires_at":        diagnostics.Store.ExpiresAt,
		"next_reconcile_at": diagnostics.Store.NextReconcileAt,
		"last_reconcile_at": diagnostics.Store.LastReconcileAt,
		"last_checked_at":   diagnostics.Runner.LastCheckedAt,
		"last_finished_at":  diagnostics.Reconcile.LastFinishedAt,
		"last_observed_at":  diagnostics.Anomaly.LastObservedAt,
	}
	for name, value := range times {
		if value == nil || value.IsZero() || value.Location() != time.UTC {
			t.Errorf("%s is not a non-zero UTC timestamp: %#v", name, value)
		}
	}
}

func adminFixtureDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate admin fixture test source")
	}
	return filepath.Join(filepath.Dir(filename), "fixtures", "admin")
}

func decodeAdminFixture[T any](t *testing.T, name string) T {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(adminFixtureDir(t), name))
	if err != nil {
		t.Fatalf("read admin fixture %s: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode admin fixture %s: %v", name, err)
	}
	if err := ensureAdminFixtureEOF(decoder); err != nil {
		t.Fatalf("decode trailing admin fixture %s: %v", name, err)
	}
	return value
}

func ensureAdminFixtureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

func readAdminFixtureObject(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	return decodeAdminFixture[map[string]json.RawMessage](t, name)
}

func decodeAdminFixtureObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode nested diagnostics object: %v", err)
	}
	return object
}

func assertAdminFixtureKeys(t *testing.T, object map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("diagnostics keys: got %v, want %v", got, want)
	}
}

func collectAdminFixtureKeys(object map[string]json.RawMessage, keys *[]string) {
	for key, raw := range object {
		*keys = append(*keys, key)
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) == nil && nested != nil {
			collectAdminFixtureKeys(nested, keys)
		}
	}
}
