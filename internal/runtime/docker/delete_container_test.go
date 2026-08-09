package docker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
)

// TestDeleteManagedContainerMissingIsSuccess 验证不存在的容器无需 stop/remove。
func TestDeleteManagedContainerMissingIsSuccess(t *testing.T) {
	engine := &fakeEngine{
		containerInspectFunc: func(
			context.Context,
			string,
			mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
		},
	}
	if err := deleteManagedContainer(
		context.Background(),
		engine,
		testSandboxID,
		5*time.Second,
	); err != nil {
		t.Fatalf("delete missing container: %v", err)
	}
}

// TestDeleteManagedContainerStoppedRemovesWithoutForce 验证 stopped 容器直接普通删除。
func TestDeleteManagedContainerStoppedRemovesWithoutForce(t *testing.T) {
	stopCalls := 0
	removeCalls := 0
	engine := &fakeEngine{
		containerInspectFunc: managedDeleteInspection(
			t,
			mobycontainer.StateExited,
		),
		containerStopFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStopOptions,
		) (mobyclient.ContainerStopResult, error) {
			stopCalls++
			return mobyclient.ContainerStopResult{}, nil
		},
		containerRemoveFunc: func(
			_ context.Context,
			containerID string,
			options mobyclient.ContainerRemoveOptions,
		) (mobyclient.ContainerRemoveResult, error) {
			removeCalls++
			if containerID != "container-id" ||
				options.Force ||
				options.RemoveVolumes ||
				options.RemoveLinks {
				t.Fatalf("remove request: id=%q options=%#v", containerID, options)
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}

	if err := deleteManagedContainer(
		context.Background(),
		engine,
		testSandboxID,
		5*time.Second,
	); err != nil {
		t.Fatalf("delete stopped container: %v", err)
	}
	if stopCalls != 0 || removeCalls != 1 {
		t.Fatalf("calls: stop=%d remove=%d", stopCalls, removeCalls)
	}
}

// TestDeleteManagedContainerRunningStopsThenRemoves 验证 running 容器先按秒级 timeout 停止。
func TestDeleteManagedContainerRunningStopsThenRemoves(t *testing.T) {
	var stopOptions mobyclient.ContainerStopOptions
	var removeOptions mobyclient.ContainerRemoveOptions
	engine := &fakeEngine{
		containerInspectFunc: managedDeleteInspection(
			t,
			mobycontainer.StateRunning,
		),
		containerStopFunc: func(
			_ context.Context,
			containerID string,
			options mobyclient.ContainerStopOptions,
		) (mobyclient.ContainerStopResult, error) {
			if containerID != "container-id" {
				t.Fatalf("stop ID: %q", containerID)
			}
			stopOptions = options
			return mobyclient.ContainerStopResult{}, nil
		},
		containerRemoveFunc: func(
			_ context.Context,
			_ string,
			options mobyclient.ContainerRemoveOptions,
		) (mobyclient.ContainerRemoveResult, error) {
			removeOptions = options
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}

	if err := deleteManagedContainer(
		context.Background(),
		engine,
		testSandboxID,
		1500*time.Millisecond,
	); err != nil {
		t.Fatalf("delete running container: %v", err)
	}
	if stopOptions.Timeout == nil || *stopOptions.Timeout != 2 {
		t.Fatalf("stop timeout: %#v", stopOptions)
	}
	if removeOptions.Force || removeOptions.RemoveVolumes {
		t.Fatalf("remove options: %#v", removeOptions)
	}
}

// TestDeleteManagedContainerForceFallback 验证 stop 失败后只 force remove 同一容器。
func TestDeleteManagedContainerForceFallback(t *testing.T) {
	stopCause := errors.New("stop failed")
	engine := &fakeEngine{
		containerInspectFunc: managedDeleteInspection(
			t,
			mobycontainer.StateRunning,
		),
		containerStopFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStopOptions,
		) (mobyclient.ContainerStopResult, error) {
			return mobyclient.ContainerStopResult{}, stopCause
		},
		containerRemoveFunc: func(
			_ context.Context,
			containerID string,
			options mobyclient.ContainerRemoveOptions,
		) (mobyclient.ContainerRemoveResult, error) {
			if containerID != "container-id" ||
				!options.Force ||
				options.RemoveVolumes {
				t.Fatalf("force remove request: id=%q options=%#v", containerID, options)
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}

	if err := deleteManagedContainer(
		context.Background(),
		engine,
		testSandboxID,
		time.Second,
	); err != nil {
		t.Fatalf("force fallback: %v", err)
	}
}

// TestDeleteManagedContainerRejectsLabelMismatch 验证非受管同名容器不触发删除。
func TestDeleteManagedContainerRejectsLabelMismatch(t *testing.T) {
	stopCalls := 0
	removeCalls := 0
	engine := &fakeEngine{
		containerInspectFunc: func(
			context.Context,
			string,
			mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{
				Container: mobycontainer.InspectResponse{
					ID:   "container-id",
					Name: "/" + containerName(testSandboxID),
					Config: &mobycontainer.Config{
						Labels: map[string]string{},
					},
					State: &mobycontainer.State{Status: mobycontainer.StateRunning},
				},
			}, nil
		},
		containerStopFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStopOptions,
		) (mobyclient.ContainerStopResult, error) {
			stopCalls++
			return mobyclient.ContainerStopResult{}, nil
		},
		containerRemoveFunc: func(
			context.Context,
			string,
			mobyclient.ContainerRemoveOptions,
		) (mobyclient.ContainerRemoveResult, error) {
			removeCalls++
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}

	err := deleteManagedContainer(
		context.Background(),
		engine,
		testSandboxID,
		time.Second,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error: got %v, want conflict", err)
	}
	if stopCalls != 0 || removeCalls != 0 {
		t.Fatalf("unsafe calls: stop=%d remove=%d", stopCalls, removeCalls)
	}
}

// TestDeleteManagedEgressSidecarStopsAndRemoves 验证只删除身份匹配的 namespace anchor。
func TestDeleteManagedEgressSidecarStopsAndRemoves(t *testing.T) {
	events := make([]string, 0, 3)
	engine := &fakeEngine{
		containerInspectFunc: func(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
			events = append(events, "inspect")
			return mobyclient.ContainerInspectResult{Container: mobycontainer.InspectResponse{
				ID: "sidecar-id", Name: "/" + egressSidecarName(testSandboxID),
				Config: &mobycontainer.Config{Labels: map[string]string{
					LabelManaged: labelManagedValue, LabelSchemaVersion: labelSchemaVersionValue,
					LabelSandboxID: testSandboxID, LabelResourceRole: resourceRoleEgressSidecar,
				}}, State: &mobycontainer.State{Status: mobycontainer.StateRunning, Running: true},
			}}, nil
		},
		containerStopFunc: func(context.Context, string, mobyclient.ContainerStopOptions) (mobyclient.ContainerStopResult, error) {
			events = append(events, "stop")
			return mobyclient.ContainerStopResult{}, nil
		},
		containerRemoveFunc: func(_ context.Context, id string, options mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
			events = append(events, "remove")
			if id != "sidecar-id" || options.Force {
				t.Fatalf("remove request: id=%q options=%#v", id, options)
			}
			return mobyclient.ContainerRemoveResult{}, nil
		},
	}
	if err := deleteManagedEgressSidecar(context.Background(), engine, testSandboxID, time.Second); err != nil {
		t.Fatalf("delete egress sidecar: %v", err)
	}
	if want := []string{"inspect", "stop", "remove"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events: got %v want %v", events, want)
	}
}

// managedDeleteInspection 返回身份匹配且处于指定状态的容器。
func managedDeleteInspection(
	t *testing.T,
	state mobycontainer.ContainerState,
) func(
	context.Context,
	string,
	mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	t.Helper()
	return func(
		context.Context,
		string,
		mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error) {
		return mobyclient.ContainerInspectResult{
			Container: mobycontainer.InspectResponse{
				ID:   "container-id",
				Name: "/" + containerName(testSandboxID),
				Config: &mobycontainer.Config{
					Labels: validTestLabels(t),
				},
				State: &mobycontainer.State{
					Status:  state,
					Running: state == mobycontainer.StateRunning,
				},
			},
		}, nil
	}
}
