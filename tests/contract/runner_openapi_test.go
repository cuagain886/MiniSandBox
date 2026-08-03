package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRunnerOpenAPIRequestMatchesExternalContract 验证内部请求字段不发生漂移。
func TestRunnerOpenAPIRequestMatchesExternalContract(t *testing.T) {
	runner := readRunnerOpenAPI(t)
	lifecycle := readLifecycleOpenAPI(t)
	required := []string{
		"  /v1/executions:",
		"    ExecuteRequest:",
		"        timeout_seconds:",
		"        background:",
	}
	for _, fragment := range required {
		if !strings.Contains(runner, fragment) {
			t.Errorf("runner OpenAPI is missing %q", fragment)
		}
		if !strings.Contains(lifecycle, fragment) && !strings.HasPrefix(fragment, "  /v1/executions") {
			t.Errorf("lifecycle OpenAPI is missing shared fragment %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"sandbox_id:", "working_dir:", "Timeout in nanoseconds", "socket_path:",
	} {
		if strings.Contains(runner, forbidden) {
			t.Errorf("runner OpenAPI exposes forbidden field %q", forbidden)
		}
	}
}

func readRunnerOpenAPI(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "api", "runner.openapi.yaml"))
	if err != nil {
		t.Fatalf("read runner OpenAPI: %v", err)
	}
	return string(content)
}
