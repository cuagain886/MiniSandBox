package contract_test

import (
	"strings"
	"testing"
)

// TestExecutionEventOpenAPISchemas 固定两份 OpenAPI 中八种事件与四种终态。
func TestExecutionEventOpenAPISchemas(t *testing.T) {
	for name, document := range map[string]string{
		"lifecycle": readLifecycleOpenAPI(t),
		"runner":    readRunnerOpenAPI(t),
	} {
		for _, fragment := range []string{
			"    ExecutionEvent:",
			"    TerminalExecutionEvent:",
			"    StartedEvent:",
			"    StdoutEvent:",
			"    StderrEvent:",
			"    OutputLimitReachedEvent:",
			"    ExitedEvent:",
			"    FailedEvent:",
			"    CancelledEvent:",
			"    TimedOutEvent:",
		} {
			if !strings.Contains(document, fragment) {
				t.Errorf("%s OpenAPI is missing %q", name, fragment)
			}
		}
	}
}
