package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/domain"
	"minisandbox/pkg/protocol"
)

// TestMapSandboxState 验证每个领域状态都有显式公共枚举映射。
func TestMapSandboxState(t *testing.T) {
	tests := []struct {
		domain   domain.SandboxState
		protocol protocol.SandboxState
	}{
		{domain.StatePending, protocol.SandboxStatePending},
		{domain.StateCreating, protocol.SandboxStateCreating},
		{domain.StateRunning, protocol.SandboxStateRunning},
		{domain.StateStopping, protocol.SandboxStateStopping},
		{domain.StateTerminated, protocol.SandboxStateTerminated},
		{domain.StateFailed, protocol.SandboxStateFailed},
	}
	for _, tt := range tests {
		got, err := mapSandboxState(tt.domain)
		if err != nil {
			t.Fatalf("map state %q: %v", tt.domain, err)
		}
		if got != tt.protocol {
			t.Fatalf("state %q: got %q, want %q", tt.domain, got, tt.protocol)
		}
	}
}

// TestMapSandboxReason 验证全部冻结 reason 必须经过 allowlist 映射。
func TestMapSandboxReason(t *testing.T) {
	reasons := []protocol.SandboxReason{
		protocol.SandboxReasonCreateAccepted,
		protocol.SandboxReasonCreatingRuntime,
		protocol.SandboxReasonWaitingRunner,
		protocol.SandboxReasonRunning,
		protocol.SandboxReasonDeleteAccepted,
		protocol.SandboxReasonDeletingRuntime,
		protocol.SandboxReasonTerminated,
		protocol.SandboxReasonImagePullFailed,
		protocol.SandboxReasonArtifactInvalid,
		protocol.SandboxReasonContainerCreateFailed,
		protocol.SandboxReasonArtifactInjectionFailed,
		protocol.SandboxReasonContainerStartFailed,
		protocol.SandboxReasonRunnerUnhealthy,
		protocol.SandboxReasonRunnerProtocolMismatch,
		protocol.SandboxReasonEgressUnhealthy,
		protocol.SandboxReasonSpecDrift,
		protocol.SandboxReasonCleanupPending,
		protocol.SandboxReasonRuntimeUnavailable,
		protocol.SandboxReasonInternalError,
		protocol.SandboxReasonRetryScheduled,
		protocol.SandboxReasonRecoveringRuntime,
		protocol.SandboxReasonRunnerHealthDegraded,
		protocol.SandboxReasonTTLExpired,
		protocol.SandboxReasonOrphanImported,
		protocol.SandboxReasonOrphanExpired,
	}
	for _, reason := range reasons {
		got, err := mapSandboxReason(string(reason))
		if err != nil {
			t.Fatalf("map reason %q: %v", reason, err)
		}
		if got != reason {
			t.Fatalf("reason %q: got %q", reason, got)
		}
	}
}

// TestMapSandboxResponseSafeFields 验证失败 message 可见而内部恢复字段不可见。
func TestMapSandboxResponseSafeFields(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	expiresAt := time.Date(2027, 7, 9, 9, 10, 11, 12, location)
	sandbox := domain.Sandbox{
		ID: "sandbox-1",
		Spec: domain.SandboxSpec{
			Image: "alpine:3.22",
			Resources: domain.ResourceLimits{
				MemoryMiB: 512,
			},
			Workspace: domain.WorkspaceSpec{
				MountPath: "/workspace",
			},
		},
		DesiredState:  domain.DesiredRunning,
		ObservedState: domain.StateFailed,
		Reason:        "RUNNER_UNHEALTHY",
		Message:       "secret runner probe detail from Store",
		RuntimeID:     "secret-runtime-id",
		SpecHash:      "secret-spec-hash",
		Revision:      42,
		ExpiresAt:     &expiresAt,
		CreatedAt:     time.Date(2027, 7, 8, 9, 10, 11, 12, location),
		UpdatedAt:     time.Date(2027, 7, 8, 9, 11, 12, 13, location),
	}

	got, err := mapSandboxResponse(sandbox)
	if err != nil {
		t.Fatalf("map sandbox: %v", err)
	}
	if got.ID != sandbox.ID ||
		got.State != protocol.SandboxStateFailed ||
		got.Reason != protocol.SandboxReasonRunnerUnhealthy ||
		got.Message != "Sandbox runner is unhealthy." ||
		got.Image != sandbox.Spec.Image {
		t.Fatalf("public response mismatch: %#v", got)
	}
	if got.ExpiresAt.Location() != time.UTC || !got.ExpiresAt.Equal(expiresAt) ||
		got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC {
		t.Fatalf("response times are not UTC: %#v", got)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, forbidden := range []string{
		"secret-runtime-id",
		"secret-spec-hash",
		"runtime_id",
		"spec_hash",
		"revision",
		"memory_mib",
		"mount_path",
		"secret runner probe detail from Store",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, encoded)
		}
	}
}

// TestMapSandboxResponseRejectsMissingExpiry 验证损坏的旧记录不能伪装成合法 Phase 3 响应。
func TestMapSandboxResponseRejectsMissingExpiry(t *testing.T) {
	_, err := mapSandboxResponse(domain.Sandbox{
		ID: "missing-expiry", Spec: domain.SandboxSpec{Image: "alpine:3.22"},
		DesiredState: domain.DesiredRunning, ObservedState: domain.StatePending,
		Reason: domain.SandboxReasonCreateAccepted,
	})
	if !errors.Is(err, errMissingSandboxExpiry) {
		t.Fatalf("missing expiry: got %v, want errMissingSandboxExpiry", err)
	}
}

// TestMapSandboxResponseRejectsUnknownValues 验证未知内部值不会成为隐式协议扩展。
func TestMapSandboxResponseRejectsUnknownValues(t *testing.T) {
	const secret = "secret-unknown-value"
	tests := []struct {
		name    string
		sandbox domain.Sandbox
		want    error
	}{
		{
			name: "state",
			sandbox: domain.Sandbox{
				ObservedState: domain.SandboxState(secret),
				Reason:        "RUNNING",
			},
			want: errUnsupportedSandboxState,
		},
		{
			name: "reason",
			sandbox: domain.Sandbox{
				ObservedState: domain.StateRunning,
				Reason:        secret,
			},
			want: errUnsupportedSandboxReason,
		},
		{
			name: "known reason with invalid state",
			sandbox: domain.Sandbox{
				ObservedState: domain.StateRunning,
				Reason:        domain.SandboxReasonTTLExpired,
				Message:       secret,
			},
			want: errUnsupportedSandboxStatus,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapSandboxResponse(tt.sandbox)
			if !errors.Is(err, tt.want) {
				t.Fatalf("map unknown %s: got %v, want %v", tt.name, err, tt.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("mapping error leaked unknown value: %v", err)
			}
			if got != (protocol.Sandbox{}) {
				t.Fatalf("unknown %s returned partial response: %#v", tt.name, got)
			}
		})
	}
}
