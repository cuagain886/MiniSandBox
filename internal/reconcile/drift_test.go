package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
	storeport "minisandbox/internal/store"
)

// TestCompareSandboxDriftBaselineAndEverySafeField 验证基线零误报及各字段漂移使用固定码。
func TestCompareSandboxDriftBaselineAndEverySafeField(t *testing.T) {
	stored, actual, expected := driftFixture(false)
	if got := CompareSandboxDrift(stored, actual, expected); len(got) != 0 {
		t.Fatalf("baseline drift: %v", got)
	}
	tests := []struct {
		field  DriftField
		mutate func(*domain.Sandbox, *ActualResourceSnapshot, *DriftExpectation)
	}{
		{DriftSpecHash, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.SpecHash = strings.Repeat("b", 64)
		}},
		{DriftImage, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.ImageReference = "different"
		}},
		{DriftPlatform, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) { a.Main.PlatformArch = "arm64" }},
		{DriftCPU, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) { a.Main.CPUQuotaMillis++ }},
		{DriftMemory, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) { a.Main.MemoryMiB++ }},
		{DriftPIDs, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) { a.Main.PIDs++ }},
		{DriftWorkspace, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.Workspace = "different"
		}},
		{DriftNetwork, func(s *domain.Sandbox, _ *ActualResourceSnapshot, _ *DriftExpectation) {
			s.Spec.Network.Outbound = true
			s.SpecHash = s.Spec.Hash()
		}},
		{DriftProcessProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.ProcessProfileValid = false
		}},
		{DriftMountProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.MountProfileValid = false
		}},
		{DriftNamespaceProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.NamespaceProfileValid = false
		}},
		{DriftPortProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.PortProfileValid = false
		}},
		{DriftDeviceProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.DeviceProfileValid = false
		}},
		{DriftPrivilegeProfile, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) { a.Main.Privileged = true }},
		{DriftRestartPolicy, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.RestartPolicy = "always"
		}},
		{DriftRunnerProtocol, func(_ *domain.Sandbox, a *ActualResourceSnapshot, _ *DriftExpectation) {
			a.Main.RunnerProtocolVersion = 99
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.field), func(t *testing.T) {
			s, a, e := driftFixture(false)
			tt.mutate(&s, &a, &e)
			if got := CompareSandboxDrift(s, a, e); !containsDrift(got, tt.field) {
				t.Fatalf("missing %s in %v", tt.field, got)
			}
		})
	}
}

// TestCompareSandboxDriftOutboundProfile 验证 sidecar protocol/policy/netns/profile 独立分类。
func TestCompareSandboxDriftOutboundProfile(t *testing.T) {
	stored, actual, expected := driftFixture(true)
	if got := CompareSandboxDrift(stored, actual, expected); len(got) != 0 {
		t.Fatalf("outbound baseline: %v", got)
	}
	actual.Egress.EgressProtocolVersion = 99
	actual.Egress.EgressPolicyHash = strings.Repeat("e", 64)
	actual.Main.NetworkPeerContainerID = "different"
	actual.Egress.CapAdd = []string{"NET_ADMIN"}
	got := CompareSandboxDrift(stored, actual, expected)
	for _, field := range []DriftField{DriftEgressProtocol, DriftEgressPolicy, DriftNetNS, DriftPrivilegeProfile} {
		if !containsDrift(got, field) {
			t.Fatalf("missing %s in %v", field, got)
		}
	}
}

type driftTestStore struct {
	record    domain.Sandbox
	updates   []storeport.ObservedUpdate
	conflicts int
}

func (s *driftTestStore) Get(context.Context, string) (domain.Sandbox, error) { return s.record, nil }
func (s *driftTestStore) UpdateObserved(_ context.Context, update storeport.ObservedUpdate) (domain.Sandbox, error) {
	s.updates = append(s.updates, update)
	if s.conflicts > 0 {
		s.conflicts--
		s.record.Revision++
		return domain.Sandbox{}, domain.ErrConflict
	}
	s.record.ObservedState, s.record.Reason, s.record.Message = update.State, update.Reason, update.Message
	s.record.Revision++
	return s.record, nil
}

// TestRecordSpecDriftUsesSafeMessageAndRetriesCAS 验证固定诊断、不含 raw values 且冲突重读。
func TestRecordSpecDriftUsesSafeMessageAndRetriesCAS(t *testing.T) {
	stored, actual, expected := driftFixture(false)
	actual.Main.ImageReference = "registry.example/secret-image:tag"
	store := &driftTestStore{record: stored, conflicts: 1}
	updated, fields, err := RecordSpecDrift(context.Background(), store, actual, expected)
	if err != nil || updated.ObservedState != domain.StateFailed || updated.Reason != domain.SandboxReasonSpecDrift || len(store.updates) != 2 || !containsDrift(fields, DriftImage) {
		t.Fatalf("record: updated=%#v fields=%v updates=%#v err=%v", updated, fields, store.updates, err)
	}
	if strings.Contains(updated.Message, "secret-image") || strings.Contains(strings.Join(driftStrings(fields), ","), "secret-image") {
		t.Fatal("drift diagnostic leaked raw value")
	}
	if updated.SpecHash != stored.SpecHash || !reflect.DeepEqual(updated.Spec, stored.Spec) {
		t.Fatal("drift recorder overwrote Store spec")
	}
}

// TestRecordSpecDriftDoesNotMutateBaselineOrDeleteIntent 验证无漂移 no-op，删除意图不被 Failed 覆盖。
func TestRecordSpecDriftDoesNotMutateBaselineOrDeleteIntent(t *testing.T) {
	stored, actual, expected := driftFixture(false)
	store := &driftTestStore{record: stored}
	if _, fields, err := RecordSpecDrift(context.Background(), store, actual, expected); err != nil || len(fields) != 0 || len(store.updates) != 0 {
		t.Fatalf("baseline write: fields=%v updates=%d err=%v", fields, len(store.updates), err)
	}
	store.record.DesiredState = domain.DesiredTerminated
	actual.Main.Privileged = true
	if _, _, err := RecordSpecDrift(context.Background(), store, actual, expected); !errors.Is(err, domain.ErrConflict) || len(store.updates) != 0 {
		t.Fatalf("delete intent overwritten: %v %#v", err, store.updates)
	}
}

func driftFixture(outbound bool) (domain.Sandbox, ActualResourceSnapshot, DriftExpectation) {
	spec := domain.SandboxSpec{
		Image: "busybox:1.36", Resources: domain.ResourceLimits{CPUQuotaMillis: 500, MemoryMiB: 256, PIDs: 64},
		Workspace: domain.WorkspaceSpec{MountPath: domain.WorkspaceMountPath}, Network: domain.NetworkSpec{Outbound: outbound},
		Platform: domain.Platform{OS: "linux", Arch: "amd64"},
	}
	stored := domain.Sandbox{ID: actualTestID, Spec: spec, SpecHash: spec.Hash(), DesiredState: domain.DesiredRunning, ObservedState: domain.StateRunning, RuntimeID: "main", Revision: 7}
	main := runtimeport.ManagedContainerObservation{
		ContainerID: "main", SandboxID: actualTestID, Role: runtimeport.ContainerRoleMain, Name: "minisandbox-" + actualTestID,
		ImageReference: spec.Image, PlatformOS: "linux", PlatformArch: "amd64", State: runtimeport.ActualRunning,
		SchemaVersion: 2, SpecHash: stored.SpecHash, RunnerProtocolVersion: 1, Workspace: "workspace", WorkspaceDestination: domain.WorkspaceMountPath,
		CPUQuotaMillis: 500, MemoryMiB: 256, PIDs: 64, ResourceProfileValid: true,
		ProcessProfileValid: true, MountProfileValid: true, NamespaceProfileValid: true, PortProfileValid: true, DeviceProfileValid: true,
		NoNewPrivileges: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "SETUID", "SETGID", "KILL"}, RestartPolicy: "no",
	}
	volume := runtimeport.ManagedVolumeObservation{VolumeName: "workspace", SandboxID: actualTestID, SchemaVersion: 2, SpecHash: stored.SpecHash}
	directory := actualDirectory(actualTestID)
	directory.Manifest.SpecHash = stored.SpecHash
	actual := ActualResourceSnapshot{SandboxID: actualTestID, Main: &main, Workspace: &volume, Directory: &directory}
	expected := DriftExpectation{}
	if outbound {
		stored.Spec.Network.Outbound = true
		stored.SpecHash = stored.Spec.Hash()
		main.SpecHash, volume.SpecHash, directory.Manifest.SpecHash = stored.SpecHash, stored.SpecHash, stored.SpecHash
		main.NetworkMode, main.NetworkPeerContainerID = "container", "egress"
		egress := actualEgress(actualTestID, "egress")
		egress.ProcessProfileValid, egress.MountProfileValid, egress.NamespaceProfileValid = true, true, true
		egress.PortProfileValid, egress.DeviceProfileValid = true, true
		egress.ReadonlyRootfs, egress.NoNewPrivileges = true, true
		egress.CapDrop, egress.CapAdd, egress.RestartPolicy = []string{"ALL"}, []string{"NET_ADMIN", "SETUID", "SETGID"}, "no"
		actual.Egress = &egress
		expected.EgressPolicyHash = egress.EgressPolicyHash
	}
	return stored, actual, expected
}

func containsDrift(fields []DriftField, wanted DriftField) bool {
	for _, field := range fields {
		if field == wanted {
			return true
		}
	}
	return false
}

func driftStrings(fields []DriftField) []string {
	result := make([]string, len(fields))
	for index, field := range fields {
		result[index] = string(field)
	}
	return result
}
