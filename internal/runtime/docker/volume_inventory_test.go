package docker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

// TestInventoryManagedVolumesMapsWorkspaceAndIgnoresExternal 验证正常卷与孤儿卷均被发现，外部卷不被接管。
func TestInventoryManagedVolumesMapsWorkspaceAndIgnoresExternal(t *testing.T) {
	orphanID := "10010203-0405-4607-8809-0a0b0c0d0e0f"
	managed := inventoryVolume(t, testSandboxID, testWorkspace)
	orphan := inventoryVolume(t, orphanID, workspaceName(orphanID))
	external := mobyvolume.Volume{Name: "external", Labels: map[string]string{}}
	engine := volumeInventoryEngine([]mobyvolume.Volume{orphan, external, managed}, nil)

	got, err := (&Runtime{engine: engine}).InventoryManagedVolumes(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	if got[0].SandboxID != testSandboxID || got[0].DiscoveryIssue != "" || got[1].SandboxID != orphanID {
		t.Fatalf("observations: %#v", got)
	}
}

// TestInventoryManagedVolumesReportsDuplicateIdentity 验证同一 ID 的两个卷不会被任意选中。
func TestInventoryManagedVolumesReportsDuplicateIdentity(t *testing.T) {
	first := inventoryVolume(t, testSandboxID, testWorkspace)
	second := inventoryVolume(t, testSandboxID, "alternate-name")
	engine := volumeInventoryEngine([]mobyvolume.Volume{first, second}, nil)
	got, err := (&Runtime{engine: engine}).InventoryManagedVolumes(context.Background())
	if err != nil || len(got) != 2 || got[0].DiscoveryIssue != runtimeport.DiscoveryDuplicateResource || got[1].DiscoveryIssue != runtimeport.DiscoveryDuplicateResource {
		t.Fatalf("duplicate inventory: %#v/%v", got, err)
	}
}

// TestInventoryManagedVolumesIsolatesUnknownSchemaAndInspectRace 验证未知 schema、消失竞态和 inspect 故障互不阻塞。
func TestInventoryManagedVolumesIsolatesUnknownSchemaAndInspectRace(t *testing.T) {
	bad := inventoryVolume(t, testSandboxID, testWorkspace)
	bad.Labels[LabelSchemaVersion] = "99"
	failed := inventoryVolume(t, "20010203-0405-4607-8809-0a0b0c0d0e0f", "failed")
	gone := inventoryVolume(t, "30010203-0405-4607-8809-0a0b0c0d0e0f", "gone")
	engine := volumeInventoryEngine([]mobyvolume.Volume{gone, failed, bad}, map[string]error{
		"gone": cerrdefs.ErrNotFound, "failed": errors.New("driver secret"),
	})
	got, err := (&Runtime{engine: engine}).InventoryManagedVolumes(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	issues := []string{got[0].DiscoveryIssue, got[1].DiscoveryIssue}
	if !reflect.DeepEqual(issues, []string{runtimeport.DiscoveryInspectUnavailable, runtimeport.DiscoverySchemaUnsupported}) {
		t.Fatalf("issues: %v", issues)
	}
}

// TestInventoryManagedVolumesRejectsWrongResourceRole 验证其他职责的受管卷不能冒充 workspace。
func TestInventoryManagedVolumesRejectsWrongResourceRole(t *testing.T) {
	volume := inventoryVolume(t, testSandboxID, testWorkspace)
	volume.Labels[LabelResourceRole] = resourceRoleMain
	got, err := (&Runtime{engine: volumeInventoryEngine([]mobyvolume.Volume{volume}, nil)}).InventoryManagedVolumes(context.Background())
	if err != nil || len(got) != 1 || got[0].DiscoveryIssue != runtimeport.DiscoveryRoleUnsupported {
		t.Fatalf("role observation: %#v/%v", got, err)
	}
}

func inventoryVolume(t *testing.T, sandboxID, name string) mobyvolume.Volume {
	t.Helper()
	labels := validTestLabels(t)
	labels[LabelSandboxID], labels[LabelWorkspace], labels[LabelResourceRole] = sandboxID, name, resourceRoleWorkspace
	return mobyvolume.Volume{Name: name, Labels: labels}
}

func volumeInventoryEngine(volumes []mobyvolume.Volume, inspectErrors map[string]error) *fakeEngine {
	byName := make(map[string]mobyvolume.Volume, len(volumes))
	for _, volume := range volumes {
		byName[volume.Name] = volume
	}
	return &fakeEngine{
		volumeListFunc: func(context.Context, mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error) {
			return mobyclient.VolumeListResult{Items: volumes}, nil
		},
		volumeInspectFunc: func(_ context.Context, name string, _ mobyclient.VolumeInspectOptions) (mobyclient.VolumeInspectResult, error) {
			if err := inspectErrors[name]; err != nil {
				return mobyclient.VolumeInspectResult{}, err
			}
			return mobyclient.VolumeInspectResult{Volume: byName[name]}, nil
		},
	}
}
