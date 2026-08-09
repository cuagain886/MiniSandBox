// Package contract_test 验证公开协议文件不会偏离当前阶段已经冻结的能力。
package contract_test

import (
	"fmt"
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

// TestPhase3IdempotencyKeyContract 固定 create header、重放和冲突语义。
func TestPhase3IdempotencyKeyContract(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	parameter := openAPISchemaBlock(
		t,
		document,
		"    IdempotencyKey:",
		"    SandboxID:",
	)
	for _, fragment := range []string{
		"      name: Idempotency-Key",
		"      in: header",
		"      required: false",
		"        minLength: 1",
		"        maxLength: 128",
		"        pattern: '^[A-Za-z0-9._:-]+$'",
		"Exactly zero or one field-value is accepted",
		"The raw key is never returned or logged",
	} {
		if !strings.Contains(parameter, fragment) {
			t.Errorf("idempotency parameter is missing %q", fragment)
		}
	}

	createPath := openAPISchemaBlock(
		t,
		document,
		"  /v1/sandboxes:",
		"  /v1/sandboxes/{sandbox_id}:",
	)
	for _, fragment := range []string{
		`$ref: "#/components/parameters/IdempotencyKey"`,
		"Without Idempotency-Key every accepted call creates a new sandbox",
		"the same key and presence-aware canonical request replays the first 202 status",
		"a different request returns 409",
		"Only committed 202",
		"Every replay receives a fresh X-Request-ID response header",
		"            Location:",
		"              required: true",
		"            X-Request-ID:",
	} {
		if !strings.Contains(createPath, fragment) {
			t.Errorf("create idempotency contract is missing %q", fragment)
		}
	}
	if !strings.Contains(document, "        - IDEMPOTENCY_CONFLICT") {
		t.Fatal("ErrorCode enum is missing IDEMPOTENCY_CONFLICT")
	}
	sandboxSchema := openAPISchemaBlock(
		t,
		document,
		"    Sandbox:",
		"      example:",
	)
	if strings.Contains(strings.ToLower(sandboxSchema), "idempotency") {
		t.Fatal("Sandbox response must not echo idempotency metadata")
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
		"RUNNER_PROTOCOL_MISMATCH",
		"EGRESS_UNHEALTHY",
		"SPEC_DRIFT",
		"CLEANUP_PENDING",
		"RUNTIME_UNAVAILABLE",
		"INTERNAL_ERROR",
		"RETRY_SCHEDULED",
		"RECOVERING_RUNTIME",
		"RUNNER_HEALTH_DEGRADED",
		"TTL_EXPIRED",
		"ORPHAN_IMPORTED",
		"ORPHAN_EXPIRED",
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

// TestPhase3ReasonAndErrorContract 固定 reason/state/message 与新增错误的 HTTP/retryable 矩阵。
func TestPhase3ReasonAndErrorContract(t *testing.T) {
	document := readLifecycleOpenAPI(t)
	reasonContracts := []struct {
		reason  string
		states  string
		message string
	}{
		{"CREATE_ACCEPTED", "Pending", "Sandbox creation has been accepted."},
		{"CREATING_RUNTIME", "Creating", "Preparing sandbox runtime."},
		{"WAITING_RUNNER", "Creating", "Waiting for sandbox runner."},
		{"RUNNING", "Running", "Sandbox is running."},
		{"DELETE_ACCEPTED", "Stopping", "Sandbox deletion has been accepted."},
		{"DELETING_RUNTIME", "Stopping", "Deleting sandbox runtime."},
		{"TERMINATED", "Terminated", "Sandbox runtime has been deleted."},
		{"IMAGE_PULL_FAILED", "Failed", "Failed to pull sandbox image."},
		{"ARTIFACT_INVALID", "Failed", "Sandbox runtime artifacts are invalid."},
		{"CONTAINER_CREATE_FAILED", "Failed", "Failed to create sandbox container."},
		{"ARTIFACT_INJECTION_FAILED", "Failed", "Failed to inject sandbox runtime artifacts."},
		{"CONTAINER_START_FAILED", "Failed", "Failed to start sandbox container."},
		{"RUNNER_UNHEALTHY", "Failed", "Sandbox runner is unhealthy."},
		{"RUNNER_PROTOCOL_MISMATCH", "Failed", "Sandbox runner protocol is incompatible."},
		{"EGRESS_UNHEALTHY", "Failed", "Sandbox outbound isolation is unhealthy."},
		{"SPEC_DRIFT", "Failed", "Sandbox runtime does not match the persisted specification."},
		{"CLEANUP_PENDING", "Failed, Stopping", "Sandbox runtime cleanup is pending."},
		{"RUNTIME_UNAVAILABLE", "Failed", "Sandbox runtime is temporarily unavailable."},
		{"INTERNAL_ERROR", "Failed", "An unexpected internal error occurred."},
		{"RETRY_SCHEDULED", "Failed, Creating, Stopping", "Sandbox reconciliation retry is scheduled."},
		{"RECOVERING_RUNTIME", "Creating", "Sandbox runtime is being recovered."},
		{"RUNNER_HEALTH_DEGRADED", "Running", "Sandbox runner health is degraded."},
		{"TTL_EXPIRED", "Stopping", "Sandbox lease has expired."},
		{"ORPHAN_IMPORTED", "Creating, Running", "Trusted sandbox resources have been imported."},
		{"ORPHAN_EXPIRED", "Stopping", "Expired sandbox resources are being deleted."},
	}
	for _, contract := range reasonContracts {
		fragment := fmt.Sprintf(
			"        %s:\n          states: [%s]\n          message: %s",
			contract.reason,
			contract.states,
			contract.message,
		)
		if !strings.Contains(document, fragment) {
			t.Errorf("reason contract is missing %s", contract.reason)
		}
	}

	errorContracts := []struct {
		code      string
		status    int
		retryable bool
		message   string
	}{
		{"INVALID_TTL", 400, false, "Sandbox TTL is invalid."},
		{"INVALID_EXPIRATION", 400, false, "Sandbox expiration is invalid."},
		{"LEASE_CONFLICT", 409, false, "Sandbox lease conflicts with the current expiration."},
		{"SANDBOX_EXPIRING", 409, false, "Sandbox is expiring or terminating."},
		{"IDEMPOTENCY_CONFLICT", 409, false, "Idempotency key conflicts with a different request."},
		{"SANDBOX_LIMIT_REACHED", 429, true, "Sandbox limit has been reached."},
		{"ADMIN_DISABLED", 404, false, "Admin API is not available."},
	}
	for _, contract := range errorContracts {
		fragment := fmt.Sprintf(
			"        %s:\n          http_status: %d\n          retryable: %t\n          message: %s",
			contract.code,
			contract.status,
			contract.retryable,
			contract.message,
		)
		if !strings.Contains(document, fragment) {
			t.Errorf("error contract is missing %s", contract.code)
		}
	}
	createPath := openAPISchemaBlock(
		t,
		document,
		"  /v1/sandboxes:",
		"  /v1/sandboxes/{sandbox_id}:",
	)
	if !strings.Contains(createPath, `"429":`) ||
		!strings.Contains(createPath, `#/components/responses/TooManyRequests`) {
		t.Fatal("create contract is missing sandbox limit response")
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
