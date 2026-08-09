package contract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestOpenAPIDocumentsParse 验证三个 OpenAPI 文件至少是结构合法且版本正确的 YAML。
func TestOpenAPIDocumentsParse(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	apiDir := filepath.Join(filepath.Dir(filename), "..", "..", "api")
	for _, name := range []string{"admin.openapi.yaml", "lifecycle.openapi.yaml", "runner.openapi.yaml"} {
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(apiDir, name))
			if err != nil {
				t.Fatalf("read OpenAPI: %v", err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(content, &document); err != nil {
				t.Fatalf("parse OpenAPI YAML: %v", err)
			}
			if document["openapi"] != "3.1.0" || document["paths"] == nil ||
				document["components"] == nil {
				t.Fatalf("incomplete OpenAPI root: %#v", document)
			}
		})
	}
}
