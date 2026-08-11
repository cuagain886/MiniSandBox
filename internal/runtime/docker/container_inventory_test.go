package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

// TestInventoryManagedContainersEmpty 验证空 daemon 返回稳定空 observation。
func TestInventoryManagedContainersEmpty(t *testing.T) {
	engine := &fakeEngine{containerListFunc: func(_ context.Context, options mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
		if !options.All {
			t.Fatal("inventory omitted stopped containers")
		}
		return mobyclient.ContainerListResult{}, nil
	}}
	got, err := (&Runtime{engine: engine}).InventoryManagedContainers(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty inventory: %#v/%v", got, err)
	}
}

// TestInventoryManagedContainersInspectsAndSortsMainAndEgress 验证 running/stopped 两类容器安全映射和排序。
func TestInventoryManagedContainersInspectsAndSortsMainAndEgress(t *testing.T) {
	main := inventoryMainContainer(t)
	egress := inventoryEgressContainer()
	engine := &fakeEngine{
		containerListFunc: func(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{Items: []mobycontainer.Summary{{ID: egress.ID}, {ID: main.ID}}}, nil
		},
		containerInspectFunc: func(_ context.Context, id string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			if id == main.ID {
				return mobyclient.ContainerInspectResult{Container: main}, nil
			}
			return mobyclient.ContainerInspectResult{Container: egress}, nil
		},
	}
	got, err := (&Runtime{engine: engine}).InventoryManagedContainers(context.Background())
	if err != nil || len(got) != 2 || got[0].Role != runtimeport.ContainerRoleEgress || got[1].Role != runtimeport.ContainerRoleMain {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	mainGot := got[1]
	if mainGot.State != runtimeport.ActualRunning || mainGot.SpecHash != testSpecHash || mainGot.NetworkMode != "none" ||
		mainGot.Workspace != testWorkspace || mainGot.Privileged || !mainGot.NoNewPrivileges || mainGot.DiscoveryIssue != "" {
		t.Fatalf("main observation: %#v", mainGot)
	}
	if got[0].EgressPolicyHash == "" || got[0].EgressProtocolVersion < 1 || got[0].State != runtimeport.ActualStopped {
		t.Fatalf("egress observation: %#v", got[0])
	}
}

// TestInventoryManagedContainersIsolatesDamageAndInspectRace 验证损坏项不阻断其他资源且消失项被忽略。
func TestInventoryManagedContainersIsolatesDamageAndInspectRace(t *testing.T) {
	valid := inventoryMainContainer(t)
	damaged := inventoryMainContainer(t)
	damaged.ID = "damaged"
	damaged.Config.Labels[LabelSchemaVersion] = "99"
	engine := &fakeEngine{
		containerListFunc: func(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{Items: []mobycontainer.Summary{{ID: "gone"}, {ID: damaged.ID}, {ID: valid.ID}}}, nil
		},
		containerInspectFunc: func(_ context.Context, id string, _ mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			switch id {
			case "gone":
				return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
			case "damaged":
				return mobyclient.ContainerInspectResult{Container: damaged}, nil
			default:
				return mobyclient.ContainerInspectResult{Container: valid}, nil
			}
		},
	}
	got, err := (&Runtime{engine: engine}).InventoryManagedContainers(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("inventory: %#v/%v", got, err)
	}
	issues := []string{got[0].DiscoveryIssue, got[1].DiscoveryIssue}
	if !reflect.DeepEqual(issues, []string{runtimeport.DiscoverySchemaUnsupported, ""}) {
		t.Fatalf("issues: %v", issues)
	}
}

// TestInventoryManagedContainersDoesNotExposeRawInspectSurface 锁定安全 observation 不含命令、env 或 bind source 字段。
func TestInventoryManagedContainersDoesNotExposeRawInspectSurface(t *testing.T) {
	typeName := reflect.TypeOf(runtimeport.ManagedContainerObservation{})
	for _, forbidden := range []string{"Env", "Command", "RawInspect", "BindSource", "HostPath"} {
		if _, ok := typeName.FieldByName(forbidden); ok {
			t.Fatalf("observation exposes %s", forbidden)
		}
	}
}

// TestInventoryManagedContainersMapsListFailure 验证全局 list 失败仍是 typed runtime unavailable。
func TestInventoryManagedContainersMapsListFailure(t *testing.T) {
	engine := &fakeEngine{containerListFunc: func(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
		return mobyclient.ContainerListResult{}, errors.New("daemon unavailable")
	}}
	if _, err := (&Runtime{engine: engine}).InventoryManagedContainers(context.Background()); err == nil {
		t.Fatal("list failure was ignored")
	}
}

// TestInventoryManagedContainersReturnsSafeInspectAnomaly 验证单项 inspect 故障不泄露错误或中断扫描。
func TestInventoryManagedContainersReturnsSafeInspectAnomaly(t *testing.T) {
	engine := &fakeEngine{
		containerListFunc: func(context.Context, mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{Items: []mobycontainer.Summary{{ID: "inspect-failed"}}}, nil
		},
		containerInspectFunc: func(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, errors.New("secret docker host path")
		},
	}
	got, err := (&Runtime{engine: engine}).InventoryManagedContainers(context.Background())
	if err != nil || len(got) != 1 || got[0].DiscoveryIssue != runtimeport.DiscoveryInspectUnavailable {
		t.Fatalf("inspect anomaly: %#v/%v", got, err)
	}
}

func inventoryMainContainer(t *testing.T) mobycontainer.InspectResponse {
	t.Helper()
	labels := validTestLabels(t)
	return mobycontainer.InspectResponse{
		ID: "main-container", Name: "/" + containerName(testSandboxID),
		Config:     &mobycontainer.Config{Labels: labels},
		HostConfig: &mobycontainer.HostConfig{NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{noNewPrivilegesSecurity}, RestartPolicy: mobycontainer.RestartPolicy{Name: mobycontainer.RestartPolicyDisabled}},
		State:      &mobycontainer.State{Status: mobycontainer.StateRunning},
		Mounts:     []mobycontainer.MountPoint{{Type: "volume", Name: testWorkspace, Destination: "/workspace"}},
	}
}

func inventoryEgressContainer() mobycontainer.InspectResponse {
	return mobycontainer.InspectResponse{
		ID: "egress-container", Name: "/" + egressSidecarName(testSandboxID),
		Config: &mobycontainer.Config{Labels: map[string]string{
			LabelManaged: labelManagedValue, LabelSchemaVersion: labelSchemaVersionValue,
			LabelSandboxID: testSandboxID, LabelResourceRole: resourceRoleEgressSidecar,
			LabelEgressPolicyHash: strings.Repeat("b", 64), LabelEgressImage: "example.invalid/egress@sha256:" + strings.Repeat("c", 64), LabelEgressProtocol: "1",
		}},
		HostConfig: &mobycontainer.HostConfig{NetworkMode: mobycontainer.NetworkMode(EgressNetworkName), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN"}, SecurityOpt: []string{noNewPrivilegesSecurity}},
		State:      &mobycontainer.State{Status: mobycontainer.StateExited},
	}
}
