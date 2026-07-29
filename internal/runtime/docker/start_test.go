package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
)

// TestStartContainerAlreadyRunningIsIdempotent 验证 running 容器不会重复 start。
func TestStartContainerAlreadyRunningIsIdempotent(t *testing.T) {
	startCalls := 0
	engine := &fakeEngine{
		containerInspectFunc: inspectionWithState(mobycontainer.StateRunning),
		containerStartFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStartOptions,
		) (mobyclient.ContainerStartResult, error) {
			startCalls++
			return mobyclient.ContainerStartResult{}, nil
		},
	}

	if err := startContainer(context.Background(), engine, "container-id"); err != nil {
		t.Fatalf("start running container: %v", err)
	}
	if startCalls != 0 {
		t.Fatalf("start calls: got %d, want 0", startCalls)
	}
}

// TestStartContainerStartsCreatedAndStopped 验证 created/exited 状态使用空安全选项启动。
func TestStartContainerStartsCreatedAndStopped(t *testing.T) {
	for _, state := range []mobycontainer.ContainerState{
		mobycontainer.StateCreated,
		mobycontainer.StateExited,
	} {
		t.Run(string(state), func(t *testing.T) {
			startCalls := 0
			engine := &fakeEngine{
				containerInspectFunc: inspectionWithState(state),
				containerStartFunc: func(
					_ context.Context,
					containerID string,
					options mobyclient.ContainerStartOptions,
				) (mobyclient.ContainerStartResult, error) {
					startCalls++
					if containerID != "container-id" ||
						options.CheckpointID != "" ||
						options.CheckpointDir != "" {
						t.Fatalf("start request: id=%q options=%#v", containerID, options)
					}
					return mobyclient.ContainerStartResult{}, nil
				},
			}

			if err := startContainer(
				context.Background(),
				engine,
				"container-id",
			); err != nil {
				t.Fatalf("start container: %v", err)
			}
			if startCalls != 1 {
				t.Fatalf("start calls: got %d, want 1", startCalls)
			}
		})
	}
}

// TestStartContainerConcurrentAlreadyStartedIsSuccess 验证 inspect/start 竞态仍保持幂等。
func TestStartContainerConcurrentAlreadyStartedIsSuccess(t *testing.T) {
	engine := &fakeEngine{
		containerInspectFunc: inspectionWithState(mobycontainer.StateCreated),
		containerStartFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStartOptions,
		) (mobyclient.ContainerStartResult, error) {
			return mobyclient.ContainerStartResult{}, cerrdefs.ErrNotModified
		},
	}

	if err := startContainer(
		context.Background(),
		engine,
		"container-id",
	); err != nil {
		t.Fatalf("concurrent start: %v", err)
	}
}

// TestStartContainerMissingIsClassified 验证 inspect 和 start 期间消失都映射 runtime missing。
func TestStartContainerMissingIsClassified(t *testing.T) {
	tests := []struct {
		name   string
		engine *fakeEngine
	}{
		{
			name: "inspect missing",
			engine: &fakeEngine{
				containerInspectFunc: func(
					context.Context,
					string,
					mobyclient.ContainerInspectOptions,
				) (mobyclient.ContainerInspectResult, error) {
					return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
				},
			},
		},
		{
			name: "missing during start",
			engine: &fakeEngine{
				containerInspectFunc: inspectionWithState(mobycontainer.StateCreated),
				containerStartFunc: func(
					context.Context,
					string,
					mobyclient.ContainerStartOptions,
				) (mobyclient.ContainerStartResult, error) {
					return mobyclient.ContainerStartResult{}, cerrdefs.ErrNotFound
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := startContainer(context.Background(), tt.engine, "container-id")
			var missing *RuntimeMissingError
			if !errors.As(err, &missing) || !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error: got %T %v, want runtime missing", err, err)
			}
		})
	}
}

// TestStartContainerRejectsUnsafeStates 验证 paused 等状态不会被擅自修复。
func TestStartContainerRejectsUnsafeStates(t *testing.T) {
	for _, state := range []mobycontainer.ContainerState{
		mobycontainer.StatePaused,
		mobycontainer.StateRestarting,
		mobycontainer.StateRemoving,
		mobycontainer.StateDead,
	} {
		t.Run(string(state), func(t *testing.T) {
			err := startContainer(
				context.Background(),
				&fakeEngine{containerInspectFunc: inspectionWithState(state)},
				"container-id",
			)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("error: got %v, want conflict", err)
			}
		})
	}
}

// TestStartContainerMapsStartFailure 验证 start Engine 故障保留 cause 且不泄露详情。
func TestStartContainerMapsStartFailure(t *testing.T) {
	cause := errors.New("start daemon detail")
	engine := &fakeEngine{
		containerInspectFunc: inspectionWithState(mobycontainer.StateCreated),
		containerStartFunc: func(
			context.Context,
			string,
			mobyclient.ContainerStartOptions,
		) (mobyclient.ContainerStartResult, error) {
			return mobyclient.ContainerStartResult{}, cause
		},
	}

	err := startContainer(context.Background(), engine, "container-id")
	var startFailed *ContainerStartFailedError
	if !errors.As(err, &startFailed) || !errors.Is(err, cause) {
		t.Fatalf("error: got %T %v", err, err)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("start error exposed daemon detail")
	}
}

// inspectionWithState 返回指定 Docker 状态的 inspect stub。
func inspectionWithState(
	state mobycontainer.ContainerState,
) func(
	context.Context,
	string,
	mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	return func(
		context.Context,
		string,
		mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error) {
		return mobyclient.ContainerInspectResult{
			Container: mobycontainer.InspectResponse{
				ID: "container-id",
				State: &mobycontainer.State{
					Status:  state,
					Running: state == mobycontainer.StateRunning,
				},
			},
		}, nil
	}
}
