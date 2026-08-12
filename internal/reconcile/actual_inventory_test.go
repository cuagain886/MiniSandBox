package reconcile

import (
	"strings"
	"testing"

	runtimeport "minisandbox/internal/runtime"
)

const actualTestID = "00010203-0405-4607-8809-0a0b0c0d0e0f"

// TestAggregateActualResourcesBuildsInboundAndOutboundBundles 验证公开 outbound 语义决定 sidecar 应存在或缺失。
func TestAggregateActualResourcesBuildsInboundAndOutboundBundles(t *testing.T) {
	inboundID := actualTestID
	outboundID := "10010203-0405-4607-8809-0a0b0c0d0e0f"
	mainInbound := actualMain(inboundID, "main-in", "none")
	mainOutbound := actualMain(outboundID, "main-out", "container")
	mainOutbound.NetworkPeerContainerID = "egress-out"
	egress := actualEgress(outboundID, "egress-out")
	volumes := []runtimeport.ManagedVolumeObservation{actualVolume(outboundID), actualVolume(inboundID)}
	directories := []runtimeport.RuntimeDirectoryObservation{actualDirectory(outboundID), actualDirectory(inboundID)}

	got := AggregateActualResources([]runtimeport.ManagedContainerObservation{mainOutbound, egress, mainInbound}, volumes, directories)
	if len(got.Sandboxes) != 2 || got.Sandboxes[0].SandboxID != inboundID || got.Sandboxes[1].SandboxID != outboundID {
		t.Fatalf("sorted inventory: %#v", got)
	}
	for _, snapshot := range got.Sandboxes {
		if snapshot.Main == nil || snapshot.Workspace == nil || snapshot.Directory == nil || len(snapshot.Anomalies) != 0 {
			t.Fatalf("bundle: %#v", snapshot)
		}
	}
	if got.Sandboxes[0].Egress != nil || got.Sandboxes[1].Egress == nil {
		t.Fatalf("egress mapping: %#v", got.Sandboxes)
	}
}

// TestAggregateActualResourcesReportsDuplicatesAndOrphanSidecar 验证每个职责至多一个且孤儿 sidecar 不会被信任。
func TestAggregateActualResourcesReportsDuplicatesAndOrphanSidecar(t *testing.T) {
	orphanID := "10010203-0405-4607-8809-0a0b0c0d0e0f"
	got := AggregateActualResources(
		[]runtimeport.ManagedContainerObservation{actualMain(actualTestID, "a", "none"), actualMain(actualTestID, "b", "none"), actualEgress(orphanID, "sidecar")},
		[]runtimeport.ManagedVolumeObservation{actualVolume(actualTestID), actualVolume(actualTestID)},
		[]runtimeport.RuntimeDirectoryObservation{actualDirectory(actualTestID), actualDirectory(actualTestID)},
	)
	if len(got.Sandboxes) != 2 || got.Sandboxes[0].Main != nil || got.Sandboxes[0].Workspace != nil || got.Sandboxes[0].Directory != nil {
		t.Fatalf("duplicate snapshot: %#v", got)
	}
	codes := anomalyCodes(got)
	for _, want := range []ActualAnomalyCode{ActualAnomalyDuplicateMain, ActualAnomalyDuplicateWorkspace, ActualAnomalyDuplicateDirectory, ActualAnomalyOrphanEgress} {
		if !codes[want] {
			t.Fatalf("missing %s in %#v", want, got)
		}
	}
}

// TestAggregateActualResourcesReportsHashSchemaAndNetNSContradictions 验证三类跨资源矛盾形成 typed anomaly。
func TestAggregateActualResourcesReportsHashSchemaAndNetNSContradictions(t *testing.T) {
	main := actualMain(actualTestID, "main", "container")
	main.NetworkPeerContainerID = "wrong-peer"
	egress := actualEgress(actualTestID, "egress")
	egress.SchemaVersion = 1
	egress.EgressProtocolVersion = 2
	egress.EgressPolicyHash = "invalid"
	volume := actualVolume(actualTestID)
	volume.SpecHash = strings.Repeat("b", 64)
	directory := actualDirectory(actualTestID)
	directory.Manifest.SpecHash = strings.Repeat("c", 64)
	directory.Manifest.SandboxID = "10010203-0405-4607-8809-0a0b0c0d0e0f"
	got := AggregateActualResources([]runtimeport.ManagedContainerObservation{main, egress}, []runtimeport.ManagedVolumeObservation{volume}, []runtimeport.RuntimeDirectoryObservation{directory})
	codes := anomalyCodes(got)
	for _, want := range []ActualAnomalyCode{ActualAnomalyNetNSConflict, ActualAnomalySchemaConflict, ActualAnomalySpecHashConflict, ActualAnomalyIdentityConflict, ActualAnomalyProtocolConflict, ActualAnomalyPolicyConflict} {
		if !codes[want] {
			t.Fatalf("missing %s in %#v", want, got)
		}
	}
}

// TestAggregateActualResourcesCopiesInputsAndScopesDamage 验证输出不共享输入 slice/pointer 且无 ID 损坏项独立保存。
func TestAggregateActualResourcesCopiesInputsAndScopesDamage(t *testing.T) {
	main := actualMain(actualTestID, "main", "none")
	main.CapDrop = []string{"ALL"}
	directory := actualDirectory(actualTestID)
	damaged := runtimeport.ManagedContainerObservation{DiscoveryIssue: runtimeport.DiscoveryLabelsInvalid}
	got := AggregateActualResources([]runtimeport.ManagedContainerObservation{main, damaged}, []runtimeport.ManagedVolumeObservation{actualVolume(actualTestID)}, []runtimeport.RuntimeDirectoryObservation{directory})
	main.CapDrop[0] = "MUTATED"
	directory.Manifest.SpecHash = "mutated"
	if got.Sandboxes[0].Main.CapDrop[0] != "ALL" || got.Sandboxes[0].Directory.Manifest.SpecHash != strings.Repeat("a", 64) || len(got.UnscopedAnomalies) != 1 {
		t.Fatalf("mutable/scoped result: %#v", got)
	}
	first := got
	second := AggregateActualResources([]runtimeport.ManagedContainerObservation{actualMain(actualTestID, "main", "none")}, []runtimeport.ManagedVolumeObservation{actualVolume(actualTestID)}, []runtimeport.RuntimeDirectoryObservation{actualDirectory(actualTestID)})
	if first.Sandboxes[0].SandboxID != second.Sandboxes[0].SandboxID {
		t.Fatal("unstable ordering")
	}
}

func actualMain(id, containerID, networkMode string) runtimeport.ManagedContainerObservation {
	return runtimeport.ManagedContainerObservation{ContainerID: containerID, SandboxID: id, Role: runtimeport.ContainerRoleMain, State: runtimeport.ActualRunning, SchemaVersion: 2, SpecHash: strings.Repeat("a", 64), RunnerProtocolVersion: 1, NetworkMode: networkMode}
}

func actualEgress(id, containerID string) runtimeport.ManagedContainerObservation {
	return runtimeport.ManagedContainerObservation{ContainerID: containerID, SandboxID: id, Role: runtimeport.ContainerRoleEgress, State: runtimeport.ActualRunning, SchemaVersion: 2, EgressProtocolVersion: 1, EgressPolicyHash: strings.Repeat("d", 64), NetworkMode: "managed-egress"}
}

func actualVolume(id string) runtimeport.ManagedVolumeObservation {
	return runtimeport.ManagedVolumeObservation{VolumeName: "workspace-" + id, SandboxID: id, SchemaVersion: 2, SpecHash: strings.Repeat("a", 64)}
}

func actualDirectory(id string) runtimeport.RuntimeDirectoryObservation {
	manifest := runtimeport.LeaseManifest{SchemaVersion: runtimeport.LeaseManifestSchemaVersion, SandboxID: id, SpecHash: strings.Repeat("a", 64)}
	return runtimeport.RuntimeDirectoryObservation{SandboxID: id, DirectoryPresent: true, ManifestPresent: true, Manifest: &manifest}
}

func anomalyCodes(inventory ActualResourceInventory) map[ActualAnomalyCode]bool {
	result := map[ActualAnomalyCode]bool{}
	for _, snapshot := range inventory.Sandboxes {
		for _, anomaly := range snapshot.Anomalies {
			result[anomaly.Code] = true
		}
	}
	return result
}
