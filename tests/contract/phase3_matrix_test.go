package contract_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/pkg/protocol"
	sdk "minisandbox/sdk/go"
)

// phase3Matrix 是 Phase 3 公共契约和 schema upgrade fixture 的统一索引。
type phase3Matrix struct {
	ContractVersion string               `json:"contract_version"`
	Cases           []phase3ContractCase `json:"cases"`
	Migration       phase3MigrationCase  `json:"migration"`
}

// phase3ContractCase 把一个能力的正反 fixture 与 HTTP surface 关联起来。
type phase3ContractCase struct {
	Name          string `json:"name"`
	Positive      string `json:"positive"`
	Negative      string `json:"negative"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	SuccessStatus int    `json:"success_status"`
}

// phase3MigrationCase 描述 Phase 2 fixture 的预期升级边界。
type phase3MigrationCase struct {
	Fixture     string `json:"fixture"`
	Snapshot    string `json:"snapshot"`
	FromVersion int64  `json:"from_version"`
	ToVersion   int64  `json:"to_version"`
}

// phase3DomainSnapshot 是迁移后可审查的稳定领域映射，不包含动态 migration clock。
type phase3DomainSnapshot struct {
	Records []phase3DomainRecord `json:"records"`
}

// phase3DomainRecord 固定旧记录升级后的状态、revision、来源和 expiry 策略。
type phase3DomainRecord struct {
	ID            string `json:"id"`
	DesiredState  string `json:"desired_state"`
	ObservedState string `json:"observed_state"`
	Revision      uint64 `json:"revision"`
	ExpiryPolicy  string `json:"expiry_policy"`
	Origin        string `json:"origin"`
}

// TestPhase3ContractMatrix 用统一索引锁定 fixture 正反例和 OpenAPI HTTP surface。
func TestPhase3ContractMatrix(t *testing.T) {
	matrix := readPhase3Matrix(t)
	if matrix.ContractVersion != "phase3-v1" || len(matrix.Cases) != 5 {
		t.Fatalf("unexpected matrix identity: %#v", matrix)
	}
	wantNames := []string{"create", "diagnostics", "error", "idempotency", "renew"}
	gotNames := make([]string, 0, len(matrix.Cases))
	for _, contractCase := range matrix.Cases {
		gotNames = append(gotNames, contractCase.Name)
		t.Run(contractCase.Name+"/positive", func(t *testing.T) {
			validatePhase3Fixture(t, contractCase.Name, contractCase.Positive, true)
		})
		t.Run(contractCase.Name+"/negative", func(t *testing.T) {
			validatePhase3Fixture(t, contractCase.Name, contractCase.Negative, false)
		})
		t.Run(contractCase.Name+"/openapi", func(t *testing.T) {
			document := readLifecycleOpenAPI(t)
			if contractCase.Name == "diagnostics" {
				document = readAdminOpenAPI(t)
			}
			if !strings.Contains(document, "  "+contractCase.Path+":") ||
				!strings.Contains(document, "    "+strings.ToLower(contractCase.Method)+":") ||
				!strings.Contains(document, fmt.Sprintf("\"%d\":", contractCase.SuccessStatus)) {
				t.Fatalf("HTTP contract drift for %#v", contractCase)
			}
		})
	}
	slices.Sort(gotNames)
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("matrix cases: got %v, want %v", gotNames, wantNames)
	}
}

// validatePhase3Fixture 对每类 fixture 应用协议本身的语义，而不是只检查 JSON 可解析。
func validatePhase3Fixture(t *testing.T, kind, name string, positive bool) {
	t.Helper()
	content := readPhase3Fixture(t, name)
	switch kind {
	case "create":
		request, err := strictDecode[protocol.CreateSandboxRequest](content)
		valid := err == nil && request.Image != "" &&
			(request.TTLSeconds == nil || *request.TTLSeconds >= 60 && *request.TTLSeconds <= 86_400)
		assertFixturePolarity(t, valid, positive, err)
	case "renew":
		request, err := strictDecode[protocol.RenewSandboxRequest](content)
		assertFixturePolarity(t, err == nil && !request.ExpiresAt.IsZero(), positive, err)
	case "idempotency":
		if positive {
			accepted := readPhase3Fixture(t, "../lifecycle/create-accepted.json")
			var sandbox protocol.Sandbox
			err := json.Unmarshal(content, &sandbox)
			assertFixturePolarity(t, err == nil && bytes.Equal(content, accepted), true, err)
			return
		}
		response, err := strictDecode[errorFixture](content)
		isConflict := err == nil && response.Error.Code == string(protocol.ErrorCodeIdempotencyConflict) &&
			response.Error.Retryable != nil && !*response.Error.Retryable
		assertFixturePolarity(t, !isConflict, false, err)
	case "diagnostics":
		diagnostics, err := strictDecode[protocol.SandboxDiagnostics](content)
		assertFixturePolarity(t, err == nil && diagnosticsSectionsComplete(diagnostics), positive, err)
	case "error":
		response, err := strictDecode[errorFixture](content)
		valid := err == nil && response.Error.Code != "" && response.Error.Message != "" &&
			response.Error.RequestID != "" && response.Error.Retryable != nil
		assertFixturePolarity(t, valid, positive, err)
	default:
		t.Fatalf("unknown Phase 3 fixture kind %q", kind)
	}
}

// assertFixturePolarity 要求 positive 被接受、negative 被语义拒绝。
func assertFixturePolarity(t *testing.T, valid, positive bool, decodeErr error) {
	t.Helper()
	if valid != positive {
		t.Fatalf("fixture validity=%t, want %t (decode error: %v)", valid, positive, decodeErr)
	}
}

// strictDecode 拒绝未知字段和尾随 JSON document。
func strictDecode[T any](content []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return value, errors.New("trailing JSON document")
	}
	return value, nil
}

// TestPhase3SDKUsesMatrix 验证 SDK 的 create/idempotency/renew wire 与同一 fixture 对齐。
func TestPhase3SDKUsesMatrix(t *testing.T) {
	createRequest := decodePhase3Fixture[protocol.CreateSandboxRequest](t, "../lifecycle/create-request.json")
	createResponse := readPhase3Fixture(t, "../lifecycle/create-accepted.json")
	renewRequest := decodePhase3Fixture[protocol.RenewSandboxRequest](t, "../lifecycle/renew-request.json")
	renewResponse := readPhase3Fixture(t, "../lifecycle/renew-success.json")
	call := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call++
		switch call {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/sandboxes" ||
				request.Header.Get("Idempotency-Key") != "phase3.matrix:1" {
				t.Errorf("unexpected create request: %s %s %#v", request.Method, request.URL.Path, request.Header)
			}
			got, err := strictDecode[protocol.CreateSandboxRequest](readRequestBody(t, request))
			if err != nil || !reflect.DeepEqual(got, createRequest) {
				t.Errorf("create wire drift: %#v, %v", got, err)
			}
			return fixtureResponse(http.StatusAccepted, createResponse), nil
		case 2:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/sandboxes/sbx-create/renew" {
				t.Errorf("unexpected renew request: %s %s", request.Method, request.URL.Path)
			}
			got, err := strictDecode[protocol.RenewSandboxRequest](readRequestBody(t, request))
			if err != nil || !got.ExpiresAt.Equal(renewRequest.ExpiresAt) {
				t.Errorf("renew wire drift: %#v, %v", got, err)
			}
			return fixtureResponse(http.StatusOK, renewResponse), nil
		default:
			return nil, errors.New("unexpected SDK matrix request")
		}
	})
	client := sdk.NewClient("http://minisandbox", &http.Client{Transport: transport})
	created, err := client.CreateSandboxWithOptions(context.Background(), sdk.CreateSandboxRequest{Image: createRequest.Image}, sdk.CreateSandboxOptions{IdempotencyKey: "phase3.matrix:1"})
	if err != nil || created.ID != "sbx-create" {
		t.Fatalf("SDK create matrix: %#v, %v", created, err)
	}
	renewed, err := client.RenewSandbox(context.Background(), created.ID, sdk.RenewSandboxRequest{ExpiresAt: renewRequest.ExpiresAt})
	if err != nil || !renewed.ExpiresAt.Equal(renewRequest.ExpiresAt) || call != 2 {
		t.Fatalf("SDK renew matrix: %#v, %v, calls=%d", renewed, err, call)
	}
}

// readRequestBody 读取内存 transport 收到的 SDK body。
func readRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	content, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read SDK request body: %v", err)
	}
	return content
}

// TestPhase3ReasonMatrix 固定 protocol 常量与 OpenAPI reason enum 的一一对应。
func TestPhase3ReasonMatrix(t *testing.T) {
	reasons := []protocol.SandboxReason{
		protocol.SandboxReasonCreateAccepted, protocol.SandboxReasonCreatingRuntime,
		protocol.SandboxReasonWaitingRunner, protocol.SandboxReasonRunning,
		protocol.SandboxReasonDeleteAccepted, protocol.SandboxReasonDeletingRuntime,
		protocol.SandboxReasonTerminated, protocol.SandboxReasonImagePullFailed,
		protocol.SandboxReasonArtifactInvalid, protocol.SandboxReasonContainerCreateFailed,
		protocol.SandboxReasonArtifactInjectionFailed, protocol.SandboxReasonContainerStartFailed,
		protocol.SandboxReasonRunnerUnhealthy, protocol.SandboxReasonRunnerProtocolMismatch,
		protocol.SandboxReasonEgressUnhealthy, protocol.SandboxReasonSpecDrift,
		protocol.SandboxReasonCleanupPending, protocol.SandboxReasonRuntimeUnavailable,
		protocol.SandboxReasonInternalError, protocol.SandboxReasonRetryScheduled,
		protocol.SandboxReasonRecoveringRuntime, protocol.SandboxReasonRunnerHealthDegraded,
		protocol.SandboxReasonTTLExpired, protocol.SandboxReasonOrphanImported,
		protocol.SandboxReasonOrphanExpired,
	}
	document := readLifecycleOpenAPI(t)
	seen := make(map[protocol.SandboxReason]struct{}, len(reasons))
	for _, reason := range reasons {
		if reason == "" {
			t.Fatal("protocol contains empty reason")
		}
		if _, duplicate := seen[reason]; duplicate {
			t.Fatalf("duplicate protocol reason %q", reason)
		}
		seen[reason] = struct{}{}
		if !strings.Contains(document, "        - "+string(reason)) {
			t.Errorf("OpenAPI reason enum is missing %s", reason)
		}
	}
}

// TestPhase2DatabaseFixtureUpgradeMatrix 验证真实 v1 fixture 升级到当前 schema 和领域映射。
func TestPhase2DatabaseFixtureUpgradeMatrix(t *testing.T) {
	matrix := readPhase3Matrix(t)
	if matrix.Migration.FromVersion != 1 || matrix.Migration.ToVersion != 4 {
		t.Fatalf("unexpected migration matrix: %#v", matrix.Migration)
	}
	path := filepath.Join(t.TempDir(), "phase2.db")
	seed, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	fixture := readPhase3Fixture(t, matrix.Migration.Fixture)
	if _, err := seed.Exec(string(fixture)); err != nil {
		_ = seed.Close()
		t.Fatalf("apply Phase 2 database fixture: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	before := time.Now().UTC()
	store, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatalf("open Store fixture: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("migrate Phase 2 fixture: %v", err)
	}
	after := time.Now().UTC()
	records, err := store.ListAll(context.Background())
	if err != nil {
		_ = store.Close()
		t.Fatalf("map migrated records: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated Store: %v", err)
	}

	snapshot := decodePhase3Fixture[phase3DomainSnapshot](t, matrix.Migration.Snapshot)
	if len(records) != len(snapshot.Records) {
		t.Fatalf("mapped record count: got %d, want %d", len(records), len(snapshot.Records))
	}
	for index, expected := range snapshot.Records {
		got := records[index]
		if got.ID != expected.ID || string(got.DesiredState) != expected.DesiredState ||
			string(got.ObservedState) != expected.ObservedState || got.Revision != expected.Revision ||
			string(got.Origin) != expected.Origin || got.ExpiresAt == nil || got.RetryAttempt != 0 ||
			got.NextReconcileAt != nil || got.LastReconcileAt != nil || got.HealthFailureCount != 0 {
			t.Fatalf("domain mapping drift at %d:\n got: %#v\nwant: %#v", index, got, expected)
		}
		assertMigratedExpiry(t, got, expected.ExpiryPolicy, before, after)
	}
	assertCurrentPhase3Schema(t, path, matrix.Migration.ToVersion)
}

// assertMigratedExpiry 校验三种已冻结的 v1 backfill 策略。
func assertMigratedExpiry(t *testing.T, record domain.Sandbox, policy string, before, after time.Time) {
	t.Helper()
	switch policy {
	case "migration_time":
		if record.ExpiresAt.Before(before) || record.ExpiresAt.After(after) {
			t.Fatalf("%s migration expiry %s outside [%s,%s]", record.ID, record.ExpiresAt, before, after)
		}
	case "migration_time_plus_default_ttl":
		minimum, maximum := before.Add(30*time.Minute), after.Add(30*time.Minute)
		if record.ExpiresAt.Before(minimum) || record.ExpiresAt.After(maximum) {
			t.Fatalf("%s default expiry %s outside [%s,%s]", record.ID, record.ExpiresAt, minimum, maximum)
		}
	case "last_transition_at":
		if !record.ExpiresAt.Equal(record.LastTransitionAt) {
			t.Fatalf("%s expiry %s does not match transition %s", record.ID, record.ExpiresAt, record.LastTransitionAt)
		}
	default:
		t.Fatalf("unknown expiry policy %q", policy)
	}
}

// assertCurrentPhase3Schema 固定最终版本和两个 Phase 3 附加表。
func assertCurrentPhase3Schema(t *testing.T, path string, wantVersion int64) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("reopen migrated fixture: %v", err)
	}
	defer db.Close()
	var version int64
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != wantVersion {
		t.Fatalf("schema version: got %d/%v, want %d", version, err, wantVersion)
	}
	for _, table := range []string{"idempotency_records", "runtime_anomalies"} {
		var name string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil || name != table {
			t.Fatalf("schema table %s: got %q/%v", table, name, err)
		}
	}
}

// readPhase3Matrix 严格读取统一 matrix manifest。
func readPhase3Matrix(t *testing.T) phase3Matrix {
	t.Helper()
	return decodePhase3Fixture[phase3Matrix](t, "matrix.json")
}

// decodePhase3Fixture 严格解码 Phase 3 fixture。
func decodePhase3Fixture[T any](t *testing.T, name string) T {
	t.Helper()
	value, err := strictDecode[T](readPhase3Fixture(t, name))
	if err != nil {
		t.Fatalf("decode Phase 3 fixture %s: %v", name, err)
	}
	return value
}

// readPhase3Fixture 读取 phase3 目录内或 manifest 明确引用的相邻 fixture。
func readPhase3Fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Phase 3 matrix test")
	}
	root := filepath.Join(filepath.Dir(filename), "fixtures", "phase3")
	content, err := os.ReadFile(filepath.Clean(filepath.Join(root, filepath.FromSlash(name))))
	if err != nil {
		t.Fatalf("read Phase 3 fixture %s: %v", name, err)
	}
	return bytes.TrimSpace(content)
}
