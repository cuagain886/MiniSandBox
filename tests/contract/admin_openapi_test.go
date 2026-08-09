package contract_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestAdminOpenAPISurface 固定只读诊断 path、认证声明和默认关闭语义。
func TestAdminOpenAPISurface(t *testing.T) {
	document := readAdminOpenAPI(t)
	for _, fragment := range []string{
		"  /v1/admin/sandboxes/{sandbox_id}/diagnostics:",
		"      operationId: getSandboxDiagnostics",
		"        - AdminBearer: []",
		`$ref: "#/components/schemas/SandboxDiagnostics"`,
		"        \"401\":",
		"        \"404\":",
		"route_registered: false",
		"token_file_read: false",
		"http_status: 404",
		"  - url: http://127.0.0.1:8080",
	} {
		if !strings.Contains(document, fragment) {
			t.Errorf("admin OpenAPI is missing %q", fragment)
		}
	}
	for _, method := range []string{"    post:", "    put:", "    patch:", "    delete:"} {
		if strings.Contains(document, method) {
			t.Errorf("read-only admin OpenAPI contains mutation method %q", method)
		}
	}
}

// TestAdminDiagnosticsSchemaContract 固定五个必填 typed section、枚举和 UTC 时间字段。
func TestAdminDiagnosticsSchemaContract(t *testing.T) {
	document := parseAdminOpenAPI(t)
	schemas := adminOpenAPISchemas(t, document)
	root := adminSchema(t, schemas, "SandboxDiagnostics")
	wantRequired := []any{
		"sandbox_id",
		"generated_at",
		"store",
		"runtime",
		"runner",
		"reconcile",
		"anomaly",
	}
	if got := root["required"]; !reflect.DeepEqual(got, wantRequired) {
		t.Fatalf("SandboxDiagnostics required: got %#v, want %#v", got, wantRequired)
	}

	wantProperties := map[string][]string{
		"SandboxDiagnostics": {
			"anomaly",
			"generated_at",
			"reconcile",
			"runner",
			"runtime",
			"sandbox_id",
			"store",
		},
		"StoreDiagnostics": {
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
			"unavailable_code",
		},
		"RuntimeDiagnostics": {
			"egress_sidecar",
			"main_container",
			"runtime_directory",
			"safe_spec_hash",
			"security_profile",
			"spec_hash_match",
			"status",
			"unavailable_code",
			"workspace_volume",
		},
		"RunnerDiagnostics": {
			"health",
			"last_checked_at",
			"status",
			"unavailable_code",
		},
		"ReconcileDiagnostics": {
			"last_code",
			"last_finished_at",
			"status",
			"unavailable_code",
		},
		"AnomalyDiagnostics": {
			"active_count",
			"classifications",
			"last_observed_at",
			"status",
			"unavailable_code",
		},
	}
	for name, want := range wantProperties {
		schema := adminSchema(t, schemas, name)
		if schema["additionalProperties"] != false {
			t.Errorf("%s must reject additional properties", name)
		}
		properties := adminSchemaProperties(t, schema)
		got := make([]string, 0, len(properties))
		for property := range properties {
			got = append(got, property)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s properties: got %v, want %v", name, got, want)
		}
	}

	wantEnums := map[string][]any{
		"DiagnosticsSectionStatus": {"available", "unavailable"},
		"DiagnosticsUnavailableCode": {
			"timeout",
			"dependency_unavailable",
			"not_collected",
			"internal_error",
		},
		"DiagnosticsDesiredState":     {"Running", "Terminated"},
		"DiagnosticsSandboxOrigin":    {"api", "recovered_orphan"},
		"DiagnosticsComputeStatus":    {"not_expected", "absent", "running", "stopped", "unknown"},
		"DiagnosticsResourcePresence": {"not_expected", "absent", "present", "unknown"},
		"DiagnosticsMatchStatus":      {"match", "mismatch", "unknown"},
		"DiagnosticsRunnerHealth":     {"unknown", "healthy", "degraded", "unhealthy", "unreachable"},
		"DiagnosticsReconcileCode": {
			"not_run",
			"converged",
			"retry_scheduled",
			"cleanup_pending",
			"spec_drift",
			"runtime_unavailable",
			"internal_error",
		},
		"DiagnosticsAnomalyClassification": {
			"incomplete_bundle",
			"unknown_schema",
			"identity_mismatch",
			"spec_hash_mismatch",
			"security_profile_mismatch",
			"network_namespace_mismatch",
			"lease_untrusted",
			"duplicate_resource",
		},
	}
	for name, want := range wantEnums {
		if got := adminSchema(t, schemas, name)["enum"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s enum: got %#v, want %#v", name, got, want)
		}
	}

	for schemaName, propertyName := range map[string]string{
		"SandboxDiagnostics":   "generated_at",
		"StoreDiagnostics":     "expires_at",
		"RunnerDiagnostics":    "last_checked_at",
		"ReconcileDiagnostics": "last_finished_at",
		"AnomalyDiagnostics":   "last_observed_at",
	} {
		property := adminProperty(t, schemas, schemaName, propertyName)
		if property["format"] != "date-time" ||
			!strings.Contains(property["description"].(string), "UTC RFC3339Nano") {
			t.Errorf("%s.%s is not an explicit UTC timestamp: %#v", schemaName, propertyName, property)
		}
	}
	storeProperties := adminSchemaProperties(t, adminSchema(t, schemas, "StoreDiagnostics"))
	for _, propertyName := range []string{"next_reconcile_at", "last_reconcile_at"} {
		property := storeProperties[propertyName].(map[string]any)
		if property["format"] != "date-time" ||
			!strings.Contains(property["description"].(string), "UTC RFC3339Nano") {
			t.Errorf("StoreDiagnostics.%s is not an explicit UTC timestamp", propertyName)
		}
	}
}

// TestAdminDiagnosticsSchemaFieldAllowlist 验证 schema property 不含任何原始运行时或秘密字段。
func TestAdminDiagnosticsSchemaFieldAllowlist(t *testing.T) {
	schemas := adminOpenAPISchemas(t, parseAdminOpenAPI(t))
	for _, schemaName := range []string{
		"SandboxDiagnostics",
		"StoreDiagnostics",
		"RuntimeDiagnostics",
		"RunnerDiagnostics",
		"ReconcileDiagnostics",
		"AnomalyDiagnostics",
	} {
		properties := adminSchemaProperties(t, adminSchema(t, schemas, schemaName))
		for property := range properties {
			lower := strings.ToLower(property)
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
					t.Errorf("%s contains forbidden property %q", schemaName, property)
				}
			}
		}
	}

	runtimeHash := adminProperty(t, schemas, "RuntimeDiagnostics", "safe_spec_hash")
	if runtimeHash["pattern"] != "^[0-9a-f]{64}$" {
		t.Fatalf("safe_spec_hash pattern: %#v", runtimeHash["pattern"])
	}
	for _, code := range []any{"SANDBOX_NOT_FOUND", "ADMIN_DISABLED"} {
		if !slices.Contains(adminSchema(t, schemas, "ErrorCode")["enum"].([]any), code) {
			t.Errorf("admin ErrorCode is missing %s", code)
		}
	}
}

func readAdminOpenAPI(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(adminOpenAPIPath(t))
	if err != nil {
		t.Fatalf("read admin OpenAPI: %v", err)
	}
	return string(content)
}

func parseAdminOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(adminOpenAPIPath(t))
	if err != nil {
		t.Fatalf("read admin OpenAPI: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse admin OpenAPI: %v", err)
	}
	return document
}

func adminOpenAPIPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate admin OpenAPI test source")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "api", "admin.openapi.yaml")
}

func adminOpenAPISchemas(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatal("admin OpenAPI components are missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("admin OpenAPI schemas are missing")
	}
	return schemas
}

func adminSchema(t *testing.T, schemas map[string]any, name string) map[string]any {
	t.Helper()
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("admin schema %s is missing", name)
	}
	return schema
}

func adminSchemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("admin schema properties are missing")
	}
	return properties
}

func adminProperty(
	t *testing.T,
	schemas map[string]any,
	schemaName string,
	propertyName string,
) map[string]any {
	t.Helper()
	properties := adminSchemaProperties(t, adminSchema(t, schemas, schemaName))
	property, ok := properties[propertyName].(map[string]any)
	if !ok {
		t.Fatalf("admin property %s.%s is missing", schemaName, propertyName)
	}
	return property
}
