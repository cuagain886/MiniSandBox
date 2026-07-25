// Package contract_test 验证公开协议文件不会偏离当前阶段已经冻结的能力。
package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPhase1LifecycleOpenAPISurface 验证 Phase 1 只公开已承诺的生命周期端点和字段。
func TestPhase1LifecycleOpenAPISurface(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	required := []string{
		"  /healthz:",
		"  /readyz:",
		"  /v1/sandboxes:",
		"  /v1/sandboxes/{sandbox_id}:",
		"      operationId: createSandbox",
		"      operationId: getSandbox",
		"      operationId: deleteSandbox",
		"      operationId: health",
		"      operationId: ready",
		"    CreateSandboxRequest:",
		"      additionalProperties: false",
	}
	for _, fragment := range required {
		if !strings.Contains(document, fragment) {
			t.Errorf("lifecycle OpenAPI is missing %q", fragment)
		}
	}

	forbidden := []string{
		"Idempotency-Key",
		"/renew:",
		"renewSandbox",
		"        command:",
		"        env:",
		"        ttl_seconds:",
		"/executions",
	}
	for _, fragment := range forbidden {
		if strings.Contains(document, fragment) {
			t.Errorf("lifecycle OpenAPI contains unsupported Phase 1 surface %q", fragment)
		}
	}
}

// TestPhase1SandboxResponseSchema 验证 create 和 get 复用完整的 Phase 1 响应 schema。
func TestPhase1SandboxResponseSchema(t *testing.T) {
	document := readLifecycleOpenAPI(t)

	if got, want := strings.Count(
		document,
		`$ref: "#/components/schemas/Sandbox"`,
	), 2; got != want {
		t.Fatalf("unexpected Sandbox schema reference count: got %d, want %d", got, want)
	}

	required := []string{
		"    SandboxState:",
		"    SandboxReason:",
		"      required: [id, state, reason, message, image, created_at, updated_at]",
		"        updated_at:",
		"        reason: CREATE_ACCEPTED",
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

	forbidden := []string{"expires_at:", "failure_reason:"}
	for _, fragment := range forbidden {
		if strings.Contains(document, fragment) {
			t.Errorf("Sandbox response contains unsupported Phase 1 field %q", fragment)
		}
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
