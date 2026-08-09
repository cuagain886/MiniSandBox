package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/config"
)

// TestPhase2OperationsGuideMatchesContracts 验证使用指南中的公共路径、字段、JSON 示例
// 和关键默认值与 OpenAPI、配置模型及示例配置保持一致。
func TestPhase2OperationsGuideMatchesContracts(t *testing.T) {
	root := documentationRepositoryRoot(t)
	document := readDocumentationFile(t, filepath.Join(root, "docs", "phase-2-operations-guide.md"))
	readme := readDocumentationFile(t, filepath.Join(root, "README.md"))
	openAPI := readDocumentationFile(t, filepath.Join(root, "api", "lifecycle.openapi.yaml"))
	configPath := filepath.Join(root, "configs", "sandboxd.example.yaml")
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load documented example config: %v", err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("validate documented example config: %v", err)
	}

	if !strings.Contains(readme, "docs/phase-2-operations-guide.md") {
		t.Fatal("root README does not link the Phase 2 operations guide")
	}
	for _, path := range []string{
		"/v1/sandboxes",
		"/v1/sandboxes/{sandbox_id}/executions",
		"/v1/sandboxes/{sandbox_id}/executions/{execution_id}",
		"/v1/sandboxes/{sandbox_id}/executions/{execution_id}/logs",
	} {
		if !strings.Contains(openAPI, "  "+path+":") || !strings.Contains(document, path) {
			t.Errorf("documented API path drifted: %s", path)
		}
	}
	for _, field := range []string{"argv", "shell", "cwd", "env", "timeout_seconds", "background", "outbound"} {
		if !strings.Contains(openAPI, "        "+field+":") || !strings.Contains(document, field) {
			t.Errorf("documented field drifted: %s", field)
		}
	}
	for _, expected := range []string{
		"`127.0.0.1:8080`", "`/workspace`", "`10m`", "`1h`", "`2s`", "`8`", "`10485760`", "`30s`",
	} {
		if !strings.Contains(document, expected) {
			t.Errorf("document is missing default %s", expected)
		}
	}
	if loaded.Server.ListenAddress != "127.0.0.1:8080" || loaded.Runner.DefaultCWD != "/workspace" ||
		loaded.Runner.DefaultTimeout != 10*time.Minute || loaded.Runner.MaxTimeout != time.Hour ||
		loaded.Runner.TerminationGrace != 2*time.Second || loaded.Runner.MaxConcurrentExecutions != 8 ||
		loaded.Runner.MaxOutputBytes != 10_485_760 || loaded.Egress.ReadyTimeout != 30*time.Second || loaded.Security.AllowOutbound {
		t.Fatalf("example configuration defaults drifted: %+v", loaded)
	}
	assertDocumentationJSONBlocks(t, document)
	assertDocumentationLinks(t, filepath.Join(root, "docs"), document)
}

func assertDocumentationJSONBlocks(t *testing.T, document string) {
	t.Helper()
	blocks := regexp.MustCompile("(?s)```json\\s*(.*?)\\s*```").FindAllStringSubmatch(document, -1)
	if len(blocks) < 5 {
		t.Fatalf("operations guide has too few JSON examples: %d", len(blocks))
	}
	for index, block := range blocks {
		if !json.Valid([]byte(block[1])) {
			t.Errorf("JSON example %d is invalid", index+1)
		}
	}
}

func assertDocumentationLinks(t *testing.T, directory, document string) {
	t.Helper()
	links := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`).FindAllStringSubmatch(document, -1)
	if len(links) < 3 {
		t.Fatalf("operations guide has too few local links: %d", len(links))
	}
	for _, match := range links {
		target := match[1]
		if strings.HasPrefix(target, "#") || strings.Contains(target, "://") {
			continue
		}
		target, _, _ = strings.Cut(target, "#")
		if _, err := os.Stat(filepath.Clean(filepath.Join(directory, filepath.FromSlash(target)))); err != nil {
			t.Errorf("broken documentation link %q", match[1])
		}
	}
}

func documentationRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate documentation contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readDocumentationFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read documentation file: %v", err)
	}
	return string(content)
}
