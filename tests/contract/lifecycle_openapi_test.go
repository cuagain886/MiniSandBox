// Package contract_test 验证公开协议文件不会偏离当前阶段已经冻结的能力。
package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPhase3LifecycleOpenAPISurface 验证 Phase 3 增量生命周期与既有 execution 契约。
func TestPhase3LifecycleOpenAPISurface(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	required := []string{
		"  /healthz:",
		"  /readyz:",
		"  /v1/sandboxes:",
		"  /v1/sandboxes/{sandbox_id}:",
		"  /v1/sandboxes/{sandbox_id}/renew:",
		"  /v1/sandboxes/{sandbox_id}/executions:",
		"      operationId: createSandbox",
		"      operationId: getSandbox",
		"      operationId: deleteSandbox",
		"      operationId: renewSandbox",
		"      operationId: health",
		"      operationId: ready",
		"      operationId: executeSandboxCommand",
		"    CreateSandboxRequest:",
		"    ExecuteRequest:",
		"      additionalProperties: false",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("lifecycle OpenAPI is missing %q", fragment)
		}
	}

	forbidden := []string{
		"Idempotency-Key",
		"working_dir:",
		"description: Timeout in nanoseconds",
		"sidecar_image:",
		"network_name:",
		"denied_cidrs:",
		"fqdn:",
		"ports:",
	}
	for _, fragment := range forbidden {
		if strings.Contains(document, fragment) {
			t.Errorf("lifecycle OpenAPI contains unsupported surface %q", fragment)
		}
	}
}

// TestPhase3SandboxResponseSchema 验证 create 和 get 复用带租约的公共响应 schema。
func TestPhase3SandboxResponseSchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	if got, want := strings.Count(
		document,
		`$ref: "#/components/schemas/Sandbox"`,
	), 3; got != want {
		t.Fatalf("unexpected Sandbox schema reference count: got %d, want %d", got, want)
	}

	required := []string{
		"    SandboxState:",
		"    SandboxReason:",
		"      required: [id, state, reason, message, image, expires_at, created_at, updated_at]",
		"        expires_at:",
		"        updated_at:",
		"        reason: CREATE_ACCEPTED",
		"        expires_at: \"2026-07-25T11:30:00Z\"",
		"        updated_at: \"2026-07-25T10:30:00Z\"",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("Sandbox response schema is missing %q", fragment)
		}
	}

	reasons := []string{
		"CREATE_ACCEPTED",
		"CREATING_RUNTIME",
		"WAITING_RUNNER",
		"RUNNING",
		"DELETE_ACCEPTED",
		"DELETING_RUNTIME",
		"TERMINATED",
		"IMAGE_PULL_FAILED",
		"ARTIFACT_INVALID",
		"CONTAINER_CREATE_FAILED",
		"ARTIFACT_INJECTION_FAILED",
		"CONTAINER_START_FAILED",
		"RUNNER_UNHEALTHY",
		"SPEC_DRIFT",
		"CLEANUP_PENDING",
		"RUNTIME_UNAVAILABLE",
		"INTERNAL_ERROR",
	}
	for _, reason := range reasons {
		if !strings.Contains(document, "        - "+reason) {
			t.Errorf("SandboxReason is missing %s", reason)
		}
	}

	forbidden := []string{"failure_reason:"}
	for _, fragment := range forbidden {
		if strings.Contains(document, fragment) {
			t.Errorf("Sandbox response contains unsupported field %q", fragment)
		}
	}
}

// TestPhase3RenewContract 固定绝对续期时间、响应状态和租约错误语义。
func TestPhase3RenewContract(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	pathBlock := openAPISchemaBlock(
		t,
		document,
		"  /v1/sandboxes/{sandbox_id}/renew:",
		"  /v1/sandboxes/{sandbox_id}/executions:",
	)
	for _, fragment := range []string{
		"      operationId: renewSandbox",
		`$ref: "#/components/schemas/RenewSandboxRequest"`,
		`$ref: "#/components/schemas/Sandbox"`,
		"        \"200\":",
		"        \"400\":",
		"        \"404\":",
		"        \"409\":",
	} {
		if !strings.Contains(pathBlock, fragment) {
			t.Errorf("renew path is missing %q", fragment)
		}
	}

	requestSchema := openAPISchemaBlock(
		t,
		document,
		"    RenewSandboxRequest:",
		"    ExecuteRequest:",
	)
	for _, fragment := range []string{
		"      additionalProperties: false",
		"      required: [expires_at]",
		"        expires_at:",
		"          format: date-time",
	} {
		if !strings.Contains(requestSchema, fragment) {
			t.Errorf("renew request schema is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"ttl_seconds", "duration", "extend_by"} {
		if strings.Contains(requestSchema, forbidden) {
			t.Errorf("renew request contains relative field %q", forbidden)
		}
	}
	for _, code := range []string{
		"INVALID_EXPIRATION",
		"LEASE_CONFLICT",
		"SANDBOX_EXPIRING",
	} {
		if !strings.Contains(document, "        - "+code) {
			t.Errorf("renew error enum is missing %s", code)
		}
	}
}

// TestPhase3CreateTTLContract 固定可选 TTL 的秒单位、边界和 presence 语义。
func TestPhase3CreateTTLContract(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	requestSchema := openAPISchemaBlock(
		t,
		document,
		"    CreateSandboxRequest:",
		"    SandboxNetworkRequest:",
	)
	for _, fragment := range []string{
		"        ttl_seconds:",
		"          type: integer",
		"          format: int64",
		"          minimum: 60",
		"          maximum: 86400",
		"          description: Optional lease duration in whole seconds; omission selects the server default",
	} {
		if !strings.Contains(requestSchema, fragment) {
			t.Errorf("create TTL schema is missing %q", fragment)
		}
	}
	if strings.Contains(requestSchema, "expires_at") {
		t.Fatal("create request must not accept absolute expires_at")
	}
}

// TestPhase1ErrorResponseSchema 验证常用错误状态全部复用同一个公共 envelope。
func TestPhase1ErrorResponseSchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	responses := []string{
		"    BadRequest:",
		"    NotFound:",
		"    Conflict:",
		"    InternalError:",
		"    ServiceUnavailable:",
	}
	for _, response := range responses {
		if !strings.Contains(document, response) {
			t.Errorf("lifecycle OpenAPI is missing response component %q", response)
		}
	}
	if got := strings.Count(
		document,
		`$ref: "#/components/schemas/ErrorResponse"`,
	); got < len(responses) {
		t.Fatalf("too few ErrorResponse references: got %d, want at least %d", got, len(responses))
	}

	required := []string{
		"    ErrorResponse:",
		"    ErrorDetail:",
		"      required: [code, message, request_id, retryable]",
		"          code: INVALID_REQUEST",
		"          message: Request is invalid.",
		"          request_id: req-01",
		"          retryable: false",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("error response schema is missing %q", fragment)
		}
	}
}

// TestPhase2ExecutionRequestSchema 固定公共执行请求的字段、单位和互斥约束。
func TestPhase2ExecutionRequestSchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	required := []string{
		"        timeout_seconds:",
		"          description: Execution timeout in seconds; zero selects the runner default",
		"      oneOf:",
		"        - required: [argv]",
		"        - required: [shell]",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("Phase 2 lifecycle schema is missing %q", fragment)
		}
	}
}

// TestPhase1ReadinessResponseSchema 验证 readyz 只公开固定组件和安全状态。
func TestPhase1ReadinessResponseSchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	required := []string{
		"    ReadinessStatus:",
		"      enum: [ready, not_ready]",
		"    ReadinessComponentName:",
		"      enum: [store, docker, artifact, recovery, worker]",
		"    ReadinessComponent:",
		"      required: [name, status]",
		"    ReadinessResponse:",
		"      required: [status, components]",
		`$ref: "#/components/schemas/ReadinessResponse"`,
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("readiness response schema is missing %q", fragment)
		}
	}
	if got, want := strings.Count(
		document,
		`$ref: "#/components/schemas/ReadinessResponse"`,
	), 2; got != want {
		t.Fatalf(
			"unexpected ReadinessResponse reference count: got %d, want %d",
			got,
			want,
		)
	}
}

// readLifecycleOpenAPI 读取当前仓库中的生命周期契约，失败时立即终止测试。
func readLifecycleOpenAPI(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	contractPath := filepath.Join(
		filepath.Dir(filename),
		"..",
		"..",
		"api",
		"lifecycle.openapi.yaml",
	)
	content, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read lifecycle OpenAPI: %v", err)
	}
	return string(content)
}

func openAPISchemaBlock(
	t *testing.T,
	document string,
	startMarker string,
	endMarker string,
) string {
	t.Helper()
	start := strings.Index(document, startMarker)
	if start < 0 {
		t.Fatalf("OpenAPI is missing schema marker %q", startMarker)
	}
	end := strings.Index(document[start+len(startMarker):], endMarker)
	if end < 0 {
		t.Fatalf("OpenAPI is missing schema marker %q", endMarker)
	}
	return document[start : start+len(startMarker)+end]
}
