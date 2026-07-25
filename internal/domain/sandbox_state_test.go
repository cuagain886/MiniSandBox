package domain

import "testing"

// TestSandboxStateTerminal 验证 Phase 1 只有 Terminated 和 Failed 被视为终态。
func TestSandboxStateTerminal(t *testing.T) {
	tests := []struct {
		name     string
		state    SandboxState
		terminal bool
	}{
		{name: "pending", state: StatePending},
		{name: "creating", state: StateCreating},
		{name: "running", state: StateRunning},
		{name: "stopping", state: StateStopping},
		{name: "terminated", state: StateTerminated, terminal: true},
		{name: "failed", state: StateFailed, terminal: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.Terminal(); got != test.terminal {
				t.Fatalf(
					"unexpected terminal result for %s: got %t, want %t",
					test.state,
					got,
					test.terminal,
				)
			}
		})
	}
}
