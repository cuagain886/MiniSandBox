package domain

import (
	"reflect"
	"testing"
)

// TestSandboxSpecValueSemantics 验证完整 resolved spec 可以安全地按值复制。
func TestSandboxSpecValueSemantics(t *testing.T) {
	original := SandboxSpec{
		Image: "alpine:3.22",
		Resources: ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: WorkspaceSpec{
			MountPath:  "/workspace",
			Persistent: false,
		},
		Network: NetworkSpec{
			Outbound: false,
		},
		Platform: Platform{
			OS:   "linux",
			Arch: "amd64",
		},
	}

	copied := original
	copied.Image = "busybox:1.37"
	copied.Resources.MemoryMiB = 512
	copied.Workspace.MountPath = "/other"
	copied.Network.Outbound = true
	copied.Platform.Arch = "arm64"

	if got, want := original.Image, "alpine:3.22"; got != want {
		t.Fatalf("copy changed original image: got %s, want %s", got, want)
	}
	if got, want := original.Resources.MemoryMiB, int64(256); got != want {
		t.Fatalf("copy changed original memory: got %d, want %d", got, want)
	}
	if got, want := original.Workspace.MountPath, "/workspace"; got != want {
		t.Fatalf("copy changed original mount path: got %s, want %s", got, want)
	}
	if original.Network.Outbound {
		t.Fatal("copy changed original outbound setting")
	}
	if got, want := original.Platform.Arch, "amd64"; got != want {
		t.Fatalf("copy changed original platform: got %s, want %s", got, want)
	}
}

// TestSandboxUsesResolvedSpec 防止领域记录退回只保存裸镜像字段。
func TestSandboxUsesResolvedSpec(t *testing.T) {
	sandboxType := reflect.TypeOf(Sandbox{})
	if _, exists := sandboxType.FieldByName("Image"); exists {
		t.Fatal("Sandbox must not expose a top-level Image field")
	}
	field, exists := sandboxType.FieldByName("Spec")
	if !exists {
		t.Fatal("Sandbox must contain Spec")
	}
	if got, want := field.Type, reflect.TypeOf(SandboxSpec{}); got != want {
		t.Fatalf("unexpected Spec type: got %s, want %s", got, want)
	}
}
