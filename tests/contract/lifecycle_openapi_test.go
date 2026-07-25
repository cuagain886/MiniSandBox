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
	document := string(content)

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
