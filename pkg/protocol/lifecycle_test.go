package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestCreateSandboxRequestJSONRoundTrip 验证网络字段缺失、false 和 true 的 wire 语义。
func TestCreateSandboxRequestJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		original CreateSandboxRequest
		encoded  string
	}{
		{
			name:     "network omitted",
			original: CreateSandboxRequest{Image: "alpine:3.22"},
			encoded:  `{"image":"alpine:3.22"}`,
		},
		{
			name: "outbound false",
			original: CreateSandboxRequest{
				Image:   "alpine:3.22",
				Network: &SandboxNetworkRequest{},
			},
			encoded: `{"image":"alpine:3.22","network":{"outbound":false}}`,
		},
		{
			name: "outbound true",
			original: CreateSandboxRequest{
				Image: "alpine:3.22",
				Network: &SandboxNetworkRequest{
					Outbound: true,
				},
			},
			encoded: `{"image":"alpine:3.22","network":{"outbound":true}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.original)
			if err != nil {
				t.Fatalf("marshal create request: %v", err)
			}
			if got := string(encoded); got != tt.encoded {
				t.Fatalf("unexpected JSON: got %s, want %s", got, tt.encoded)
			}
			var decoded CreateSandboxRequest
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal create request: %v", err)
			}
			if !reflect.DeepEqual(decoded, tt.original) {
				t.Fatalf("round trip mismatch: got %#v, want %#v", decoded, tt.original)
			}
		})
	}
}

// TestCreateSandboxRequestSurface 防止公共请求暴露 egress 实现参数。
func TestCreateSandboxRequestSurface(t *testing.T) {
	requestType := reflect.TypeOf(CreateSandboxRequest{})
	if got, want := requestType.NumField(), 2; got != want {
		t.Fatalf("unexpected field count: got %d, want %d", got, want)
	}
	for index, name := range []string{"Image", "Network"} {
		if got := requestType.Field(index).Name; got != name {
			t.Fatalf("unexpected request field: got %s, want %s", got, name)
		}
	}
	networkType := reflect.TypeOf(SandboxNetworkRequest{})
	if networkType.NumField() != 1 || networkType.Field(0).Name != "Outbound" {
		t.Fatalf("unexpected network request fields: %#v", networkType)
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
