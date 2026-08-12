package reconcile

import (
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/domain"
)

// TestRebuildResolvedSpecGoldenInboundAndOutbound 验证完整 allowlist 字段可无损重建两种网络规格。
func TestRebuildResolvedSpecGoldenInboundAndOutbound(t *testing.T) {
	for _, outbound := range []bool{false, true} {
		stored, actual, expected := driftFixture(outbound)
		got, err := RebuildResolvedSpec(actual, expected)
		if err != nil || !reflect.DeepEqual(got, stored.Spec) || got.Hash() != stored.SpecHash {
			t.Fatalf("outbound=%t spec=%#v want=%#v err=%v", outbound, got, stored.Spec, err)
		}
	}
}

// TestRebuildResolvedSpecMapsEveryDomainField 验证镜像、三项资源、workspace、network 和 platform 均来自明确事实。
func TestRebuildResolvedSpecMapsEveryDomainField(t *testing.T) {
	_, actual, expected := driftFixture(false)
	actual.Main.ImageReference = "example.invalid/tool@sha256:" + strings.Repeat("a", 64)
	actual.Main.CPUQuotaMillis, actual.Main.MemoryMiB, actual.Main.PIDs = 750, 384, 96
	spec := domain.SandboxSpec{
		Image: actual.Main.ImageReference, Resources: domain.ResourceLimits{CPUQuotaMillis: 750, MemoryMiB: 384, PIDs: 96},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath}, Platform: domain.Platform{OS: "linux", Arch: "amd64"},
	}
	hash := spec.Hash()
	actual.Main.SpecHash, actual.Workspace.SpecHash, actual.Directory.Manifest.SpecHash = hash, hash, hash
	got, err := RebuildResolvedSpec(actual, expected)
	if err != nil || !reflect.DeepEqual(got, spec) {
		t.Fatalf("mapped spec: %#v/%v", got, err)
	}
}

// TestRebuildResolvedSpecRejectsUnsupportedPlatformAndProfiles 验证未知平台与每类安全 profile 都 fail closed。
func TestRebuildResolvedSpecRejectsUnsupportedPlatformAndProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ActualResourceSnapshot)
	}{
		{"platform", func(a *ActualResourceSnapshot) { a.Main.PlatformArch = "arm64" }},
		{"process", func(a *ActualResourceSnapshot) { a.Main.ProcessProfileValid = false }},
		{"mount", func(a *ActualResourceSnapshot) { a.Main.MountProfileValid = false }},
		{"namespace", func(a *ActualResourceSnapshot) { a.Main.NamespaceProfileValid = false }},
		{"ports", func(a *ActualResourceSnapshot) { a.Main.PortProfileValid = false }},
		{"devices", func(a *ActualResourceSnapshot) { a.Main.DeviceProfileValid = false }},
		{"privileged", func(a *ActualResourceSnapshot) { a.Main.Privileged = true }},
		{"restart", func(a *ActualResourceSnapshot) { a.Main.RestartPolicy = "always" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, actual, expected := driftFixture(false)
			tt.mutate(&actual)
			if _, err := RebuildResolvedSpec(actual, expected); err == nil || TrustedSpecErrorCode(err) != trustedSpecProfileInvalid {
				t.Fatalf("unsafe profile accepted: %v", err)
			}
		})
	}
}

// TestRebuildResolvedSpecRejectsHashAndOutboundBundleMismatch 验证重算 hash、sidecar policy 与 netns 必须全部匹配。
func TestRebuildResolvedSpecRejectsHashAndOutboundBundleMismatch(t *testing.T) {
	_, actual, expected := driftFixture(false)
	actual.Main.SpecHash = strings.Repeat("f", 64)
	if _, err := RebuildResolvedSpec(actual, expected); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	_, actual, expected = driftFixture(true)
	actual.Main.NetworkPeerContainerID = "wrong"
	if _, err := RebuildResolvedSpec(actual, expected); err == nil {
		t.Fatal("netns mismatch accepted")
	}
	_, actual, expected = driftFixture(true)
	expected.EgressPolicyHash = strings.Repeat("f", 64)
	if _, err := RebuildResolvedSpec(actual, expected); err == nil {
		t.Fatal("policy mismatch accepted")
	}
}

// TestRebuildResolvedSpecNeverExposesRawValues 验证 importer 错误不回显镜像或路径。
func TestRebuildResolvedSpecNeverExposesRawValues(t *testing.T) {
	_, actual, expected := driftFixture(false)
	actual.Main.ImageReference = "secret.registry/private/image"
	actual.Main.PlatformOS = "unknown"
	_, err := RebuildResolvedSpec(actual, expected)
	if err == nil || strings.Contains(err.Error(), "secret.registry") || strings.Contains(err.Error(), actual.Main.WorkspaceDestination) {
		t.Fatalf("unsafe error: %v", err)
	}
}
