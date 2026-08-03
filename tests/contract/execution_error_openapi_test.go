package contract_test

import (
	"strings"
	"testing"
)

// TestExecutionErrorOpenAPIEnums 固定公共 execution 错误码并同步 runner 子集。
func TestExecutionErrorOpenAPIEnums(t *testing.T) {
	lifecycle := readLifecycleOpenAPI(t)
	runner := readRunnerOpenAPI(t)
	for _, code := range []string{
		"INVALID_EXECUTION_REQUEST",
		"EXECUTION_NOT_FOUND",
		"EXECUTION_LIMIT_REACHED",
		"SHELL_NOT_FOUND",
		"INVALID_CWD",
	} {
		if !strings.Contains(lifecycle, "        - "+code) ||
			!strings.Contains(runner, "        - "+code) {
			t.Errorf("shared execution error code is missing %s", code)
		}
	}
	for _, code := range []string{
		"SANDBOX_NOT_RUNNING",
		"RUNNER_UNHEALTHY",
		"RUNNER_PROTOCOL_MISMATCH",
	} {
		if !strings.Contains(lifecycle, "        - "+code) {
			t.Errorf("public execution error code is missing %s", code)
		}
	}
}
