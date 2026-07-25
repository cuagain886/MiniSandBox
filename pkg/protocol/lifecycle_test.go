package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestCreateSandboxRequestJSONRoundTrip 验证 Phase 1 创建请求只包含镜像字段。
func TestCreateSandboxRequestJSONRoundTrip(t *testing.T) {
	original := CreateSandboxRequest{Image: "alpine:3.22"}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	if got, want := string(encoded), `{"image":"alpine:3.22"}`; got != want {
		t.Fatalf("unexpected JSON: got %s, want %s", got, want)
	}

	var decoded CreateSandboxRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal create request: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, original)
	}
}

// TestCreateSandboxRequestHasOnlyImageField 防止后续实现提前暴露 Phase 1 不支持的字段。
func TestCreateSandboxRequestHasOnlyImageField(t *testing.T) {
	requestType := reflect.TypeOf(CreateSandboxRequest{})
	if got, want := requestType.NumField(), 1; got != want {
		t.Fatalf("unexpected field count: got %d, want %d", got, want)
	}
	if got, want := requestType.Field(0).Name, "Image"; got != want {
		t.Fatalf("unexpected request field: got %s, want %s", got, want)
	}
}

// TestSandboxJSONRoundTrip 验证 Phase 1 状态响应的字段名称、枚举和时间格式。
func TestSandboxJSONRoundTrip(t *testing.T) {
	timestamp := time.Date(2026, time.July, 25, 10, 30, 0, 0, time.UTC)
	original := Sandbox{
		ID:        "sbx-01",
		State:     SandboxStatePending,
		Reason:    SandboxReasonCreateAccepted,
		Message:   "Sandbox creation has been accepted.",
		Image:     "alpine:3.22",
		CreatedAt: timestamp,
		UpdatedAt: timestamp,
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	const expected = `{"id":"sbx-01","state":"Pending","reason":"CREATE_ACCEPTED","message":"Sandbox creation has been accepted.","image":"alpine:3.22","created_at":"2026-07-25T10:30:00Z","updated_at":"2026-07-25T10:30:00Z"}`
	if got := string(encoded); got != expected {
		t.Fatalf("unexpected JSON: got %s, want %s", got, expected)
	}

	var decoded Sandbox
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal sandbox: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, original)
	}
}

// TestSandboxStatusEnums 固定 Phase 1 的状态和机器可读原因集合。
func TestSandboxStatusEnums(t *testing.T) {
	states := []SandboxState{
		SandboxStatePending,
		SandboxStateCreating,
		SandboxStateRunning,
		SandboxStateStopping,
		SandboxStateTerminated,
		SandboxStateFailed,
	}
	expectedStates := []string{
		"Pending",
		"Creating",
		"Running",
		"Stopping",
		"Terminated",
		"Failed",
	}
	for index, state := range states {
		if got, want := string(state), expectedStates[index]; got != want {
			t.Errorf("unexpected state at index %d: got %s, want %s", index, got, want)
		}
	}

	reasons := []SandboxReason{
		SandboxReasonCreateAccepted,
		SandboxReasonCreatingRuntime,
		SandboxReasonWaitingRunner,
		SandboxReasonRunning,
		SandboxReasonDeleteAccepted,
		SandboxReasonDeletingRuntime,
		SandboxReasonTerminated,
		SandboxReasonImagePullFailed,
		SandboxReasonArtifactInvalid,
		SandboxReasonContainerCreateFailed,
		SandboxReasonArtifactInjectionFailed,
		SandboxReasonContainerStartFailed,
		SandboxReasonRunnerUnhealthy,
		SandboxReasonSpecDrift,
		SandboxReasonCleanupPending,
		SandboxReasonRuntimeUnavailable,
		SandboxReasonInternalError,
	}
	expectedReasons := []string{
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
	for index, reason := range reasons {
		if got, want := string(reason), expectedReasons[index]; got != want {
			t.Errorf("unexpected reason at index %d: got %s, want %s", index, got, want)
		}
	}
}
