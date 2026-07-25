package domain

import (
	"testing"
	"time"
)

// TestSandboxZeroValue 验证未持久化领域记录不会伪造状态、修订号或时间。
func TestSandboxZeroValue(t *testing.T) {
	var sandbox Sandbox

	if sandbox.ID != "" || sandbox.Reason != "" || sandbox.Message != "" {
		t.Fatal("zero-value sandbox metadata must be empty")
	}
	if sandbox.RuntimeID != "" || sandbox.SpecHash != "" {
		t.Fatal("zero-value runtime metadata must be empty")
	}
	if sandbox.DesiredState != "" || sandbox.ObservedState != "" {
		t.Fatal("zero-value lifecycle states must be empty")
	}
	if sandbox.Revision != 0 {
		t.Fatalf("zero-value revision must be 0, got %d", sandbox.Revision)
	}
	if !sandbox.CreatedAt.IsZero() ||
		!sandbox.UpdatedAt.IsZero() ||
		!sandbox.LastTransitionAt.IsZero() {
		t.Fatal("zero-value timestamps must be zero")
	}
	if sandbox.ExpiresAt != nil {
		t.Fatal("Phase 1 zero-value ExpiresAt must be nil")
	}
}

// TestSandboxCopy 验证领域记录的 Phase 1 元数据和 resolved spec 按值复制。
func TestSandboxCopy(t *testing.T) {
	createdAt := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	original := Sandbox{
		ID: "sbx-copy",
		Spec: SandboxSpec{
			Image: "alpine:3.22",
			Resources: ResourceLimits{
				CPUQuotaMillis: 500,
				MemoryMiB:      256,
				PIDs:           64,
			},
			Workspace: WorkspaceSpec{MountPath: "/workspace"},
			Platform:  Platform{OS: "linux", Arch: "amd64"},
		},
		DesiredState:     DesiredRunning,
		ObservedState:    StateCreating,
		Reason:           "CREATING_RUNTIME",
		Message:          "Sandbox runtime is being created.",
		RuntimeID:        "runtime-01",
		SpecHash:         "0123456789abcdef",
		Revision:         3,
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt.Add(time.Second),
		LastTransitionAt: createdAt.Add(time.Second),
	}

	copied := original
	if copied != original {
		t.Fatal("copied sandbox must initially equal the original")
	}

	copied.Spec.Image = "busybox:1.37"
	copied.Reason = "WAITING_RUNNER"
	copied.RuntimeID = "runtime-02"
	copied.Revision++
	copied.UpdatedAt = copied.UpdatedAt.Add(time.Second)

	if got, want := original.Spec.Image, "alpine:3.22"; got != want {
		t.Fatalf("copy changed original image: got %s, want %s", got, want)
	}
	if got, want := original.Reason, "CREATING_RUNTIME"; got != want {
		t.Fatalf("copy changed original reason: got %s, want %s", got, want)
	}
	if got, want := original.RuntimeID, "runtime-01"; got != want {
		t.Fatalf("copy changed original runtime ID: got %s, want %s", got, want)
	}
	if got, want := original.Revision, uint64(3); got != want {
		t.Fatalf("copy changed original revision: got %d, want %d", got, want)
	}
	if got, want := original.UpdatedAt, createdAt.Add(time.Second); got != want {
		t.Fatalf("copy changed original updated time: got %s, want %s", got, want)
	}
}
