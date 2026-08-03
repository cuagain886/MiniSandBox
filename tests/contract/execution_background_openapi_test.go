package contract_test

import (
	"strings"
	"testing"
)

// TestBackgroundExecutionOpenAPISchemas 固定后台资源路径和分页响应。
func TestBackgroundExecutionOpenAPISchemas(t *testing.T) {
	lifecycle := readLifecycleOpenAPI(t)
	runner := readRunnerOpenAPI(t)
	for _, fragment := range []string{
		"    ExecutionDescriptor:",
		"    ExecutionStatus:",
		"    ExecutionLogPage:",
		"      required: [events, next_cursor, complete]",
	} {
		if !strings.Contains(lifecycle, fragment) || !strings.Contains(runner, fragment) {
			t.Errorf("shared background schema is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"  /v1/sandboxes/{sandbox_id}/executions/{execution_id}:",
		"  /v1/sandboxes/{sandbox_id}/executions/{execution_id}/logs:",
	} {
		if !strings.Contains(lifecycle, fragment) {
			t.Errorf("lifecycle OpenAPI is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"  /v1/executions/{execution_id}:",
		"  /v1/executions/{execution_id}/logs:",
	} {
		if !strings.Contains(runner, fragment) {
			t.Errorf("runner OpenAPI is missing %q", fragment)
		}
	}
}
