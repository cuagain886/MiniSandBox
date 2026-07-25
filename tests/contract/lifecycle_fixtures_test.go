package contract_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"minisandbox/pkg/protocol"
)

// healthFixture 描述当前 health 成功响应，只用于校验 wire fixture。
type healthFixture struct {
	Status  string       `json:"status"`
	Service string       `json:"service"`
	Build   buildFixture `json:"build"`
}

// buildFixture 描述 health fixture 中公开的安全构建信息。
type buildFixture struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// errorFixture 描述公共错误 envelope，并用指针识别缺失的 retryable 字段。
type errorFixture struct {
	Error errorDetailFixture `json:"error"`
}

// errorDetailFixture 描述 fixture 中四个必填公共错误字段。
type errorDetailFixture struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Retryable *bool  `json:"retryable"`
}

// TestLifecycleFixtures 离线校验 Phase 1 生命周期 fixture 的字段、枚举和错误 envelope。
func TestLifecycleFixtures(t *testing.T) {
	t.Run("fixture set", func(t *testing.T) {
		entries, err := os.ReadDir(lifecycleFixtureDir(t))
		if err != nil {
			t.Fatalf("read fixture directory: %v", err)
		}
		actual := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				actual = append(actual, entry.Name())
			}
		}
		expected := []string{
			"create-accepted.json",
			"create-request.json",
			"delete-not-found.json",
			"get-running.json",
			"health-ok.json",
			"ready-unavailable.json",
		}
		slices.Sort(actual)
		if !slices.Equal(actual, expected) {
			t.Fatalf("unexpected fixture set: got %v, want %v", actual, expected)
		}
	})

	t.Run("create request", func(t *testing.T) {
		request := decodeLifecycleFixture[protocol.CreateSandboxRequest](
			t,
			"create-request.json",
		)
		if got, want := request.Image, "alpine:3.22"; got != want {
			t.Fatalf("unexpected image: got %s, want %s", got, want)
		}
	})

	t.Run("create accepted", func(t *testing.T) {
		sandbox := decodeLifecycleFixture[protocol.Sandbox](
			t,
			"create-accepted.json",
		)
		assertSandboxFixture(
			t,
			sandbox,
			protocol.SandboxStatePending,
			protocol.SandboxReasonCreateAccepted,
		)
	})

	t.Run("get running", func(t *testing.T) {
		sandbox := decodeLifecycleFixture[protocol.Sandbox](
			t,
			"get-running.json",
		)
		assertSandboxFixture(
			t,
			sandbox,
			protocol.SandboxStateRunning,
			protocol.SandboxReasonRunning,
		)
	})

	t.Run("delete not found", func(t *testing.T) {
		response := decodeLifecycleFixture[errorFixture](
			t,
			"delete-not-found.json",
		)
		assertErrorFixture(t, response, "SANDBOX_NOT_FOUND", false)
	})

	t.Run("health ok", func(t *testing.T) {
		response := decodeLifecycleFixture[healthFixture](t, "health-ok.json")
		if response.Status != "ok" {
			t.Fatalf("unexpected health status %q", response.Status)
		}
		if response.Service != "sandboxd" {
			t.Fatalf("unexpected health service %q", response.Service)
		}
		if response.Build.Version == "" {
			t.Fatal("health build version must not be empty")
		}
	})

	t.Run("ready unavailable", func(t *testing.T) {
		response := decodeLifecycleFixture[errorFixture](
			t,
			"ready-unavailable.json",
		)
		assertErrorFixture(t, response, "RUNTIME_UNAVAILABLE", true)
	})
}

// decodeLifecycleFixture 严格解码单个 fixture，拒绝未知字段和多个 JSON document。
func decodeLifecycleFixture[T any](t *testing.T, name string) T {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(lifecycleFixtureDir(t), name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()

	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("fixture %s must contain one JSON document, got %v", name, err)
	}
	return value
}

// lifecycleFixtureDir 返回相对于当前测试文件的 lifecycle fixture 目录。
func lifecycleFixtureDir(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	return filepath.Join(filepath.Dir(filename), "fixtures", "lifecycle")
}

// assertSandboxFixture 检查 Sandbox fixture 的必填字段和预期状态组合。
func assertSandboxFixture(
	t *testing.T,
	sandbox protocol.Sandbox,
	state protocol.SandboxState,
	reason protocol.SandboxReason,
) {
	t.Helper()

	if sandbox.ID == "" || sandbox.Image == "" || sandbox.Message == "" {
		t.Fatal("sandbox string fields must not be empty")
	}
	if sandbox.CreatedAt.IsZero() || sandbox.UpdatedAt.IsZero() {
		t.Fatal("sandbox timestamps must not be zero")
	}
	if sandbox.UpdatedAt.Before(sandbox.CreatedAt) {
		t.Fatal("sandbox updated_at must not precede created_at")
	}
	if sandbox.State != state || sandbox.Reason != reason {
		t.Fatalf(
			"unexpected sandbox status: got %s/%s, want %s/%s",
			sandbox.State,
			sandbox.Reason,
			state,
			reason,
		)
	}
}

// assertErrorFixture 检查错误 fixture 的必填字段和重试语义。
func assertErrorFixture(
	t *testing.T,
	response errorFixture,
	code string,
	retryable bool,
) {
	t.Helper()

	if response.Error.Code != code {
		t.Fatalf("unexpected error code: got %s, want %s", response.Error.Code, code)
	}
	if response.Error.Message == "" || response.Error.RequestID == "" {
		t.Fatal("error message and request_id must not be empty")
	}
	if response.Error.Retryable == nil {
		t.Fatal("error retryable field must be present")
	}
	if *response.Error.Retryable != retryable {
		t.Fatalf(
			"unexpected retryable value for %s: got %t, want %t",
			code,
			*response.Error.Retryable,
			retryable,
		)
	}
}
