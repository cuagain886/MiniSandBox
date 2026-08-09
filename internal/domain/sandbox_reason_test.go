package domain

import "testing"

// TestSandboxReasonContract 固定全部生命周期 reason、合法 state 和安全公共文案。
func TestSandboxReasonContract(t *testing.T) {
	tests := []struct {
		reason  string
		states  []SandboxState
		message string
	}{
		{SandboxReasonCreateAccepted, []SandboxState{StatePending}, "Sandbox creation has been accepted."},
		{SandboxReasonCreatingRuntime, []SandboxState{StateCreating}, "Preparing sandbox runtime."},
		{SandboxReasonWaitingRunner, []SandboxState{StateCreating}, "Waiting for sandbox runner."},
		{SandboxReasonRunning, []SandboxState{StateRunning}, "Sandbox is running."},
		{SandboxReasonDeleteAccepted, []SandboxState{StateStopping}, "Sandbox deletion has been accepted."},
		{SandboxReasonDeletingRuntime, []SandboxState{StateStopping}, "Deleting sandbox runtime."},
		{SandboxReasonTerminated, []SandboxState{StateTerminated}, "Sandbox runtime has been deleted."},
		{SandboxReasonImagePullFailed, []SandboxState{StateFailed}, "Failed to pull sandbox image."},
		{SandboxReasonArtifactInvalid, []SandboxState{StateFailed}, "Sandbox runtime artifacts are invalid."},
		{SandboxReasonContainerCreateFailed, []SandboxState{StateFailed}, "Failed to create sandbox container."},
		{SandboxReasonArtifactInjectionFailed, []SandboxState{StateFailed}, "Failed to inject sandbox runtime artifacts."},
		{SandboxReasonContainerStartFailed, []SandboxState{StateFailed}, "Failed to start sandbox container."},
		{SandboxReasonRunnerUnhealthy, []SandboxState{StateFailed}, "Sandbox runner is unhealthy."},
		{SandboxReasonRunnerProtocolMismatch, []SandboxState{StateFailed}, "Sandbox runner protocol is incompatible."},
		{SandboxReasonEgressUnhealthy, []SandboxState{StateFailed}, "Sandbox outbound isolation is unhealthy."},
		{SandboxReasonSpecDrift, []SandboxState{StateFailed}, "Sandbox runtime does not match the persisted specification."},
		{SandboxReasonCleanupPending, []SandboxState{StateStopping, StateFailed}, "Sandbox runtime cleanup is pending."},
		{SandboxReasonRuntimeUnavailable, []SandboxState{StateFailed}, "Sandbox runtime is temporarily unavailable."},
		{SandboxReasonInternalError, []SandboxState{StateFailed}, "An unexpected internal error occurred."},
		{SandboxReasonRetryScheduled, []SandboxState{StateCreating, StateStopping, StateFailed}, "Sandbox reconciliation retry is scheduled."},
		{SandboxReasonRecoveringRuntime, []SandboxState{StateCreating}, "Sandbox runtime is being recovered."},
		{SandboxReasonRunnerHealthDegraded, []SandboxState{StateRunning}, "Sandbox runner health is degraded."},
		{SandboxReasonTTLExpired, []SandboxState{StateStopping}, "Sandbox lease has expired."},
		{SandboxReasonOrphanImported, []SandboxState{StateCreating, StateRunning}, "Trusted sandbox resources have been imported."},
		{SandboxReasonOrphanExpired, []SandboxState{StateStopping}, "Expired sandbox resources are being deleted."},
	}
	allStates := []SandboxState{
		StatePending,
		StateCreating,
		StateRunning,
		StateStopping,
		StateTerminated,
		StateFailed,
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		if _, exists := seen[tt.reason]; exists {
			t.Fatalf("duplicate reason %q", tt.reason)
		}
		seen[tt.reason] = struct{}{}
		message, ok := SandboxReasonPublicMessage(tt.reason)
		if !ok || message != tt.message {
			t.Fatalf("message for %s: got %q/%t, want %q/true", tt.reason, message, ok, tt.message)
		}
		for _, state := range allStates {
			want := containsSandboxState(tt.states, state)
			if got := SandboxReasonStateAllowed(tt.reason, state); got != want {
				t.Errorf("reason/state %s/%s: got %t, want %t", tt.reason, state, got, want)
			}
		}
	}
}

// TestSandboxReasonContractRejectsUnknown 验证内部新值不能隐式扩展公共协议。
func TestSandboxReasonContractRejectsUnknown(t *testing.T) {
	if SandboxReasonStateAllowed("SECRET_INTERNAL_REASON", StateFailed) {
		t.Fatal("unknown reason was accepted")
	}
	if SandboxReasonStateAllowed(SandboxReasonRunning, SandboxState("SecretState")) {
		t.Fatal("unknown state was accepted")
	}
	if message, ok := SandboxReasonPublicMessage("SECRET_INTERNAL_REASON"); ok || message != "" {
		t.Fatalf("unknown reason returned public message: %q/%t", message, ok)
	}
}

func containsSandboxState(states []SandboxState, candidate SandboxState) bool {
	for _, state := range states {
		if state == candidate {
			return true
		}
	}
	return false
}
