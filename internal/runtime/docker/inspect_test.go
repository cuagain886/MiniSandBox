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
	runtimeport "minisandbox/internal/runtime"
)

// TestRuntimeInspectMissingReturnsActualMissing 验证容器不存在是无错误的实际状态。
func TestRuntimeInspectMissingReturnsActualMissing(t *testing.T) {
	engine := &fakeEngine{
		containerInspectFunc: func(
			_ context.Context,
			name string,
			_ mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			if name != containerName(testSandboxID) {
				t.Fatalf("inspect name: got %q", name)
			}
			return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
		},
	}

	actual, err := (&Runtime{engine: engine}).Inspect(
		context.Background(),
		testSandboxID,
	)
	if err != nil {
		t.Fatalf("inspect missing: %v", err)
	}
	if actual != (runtimeport.ActualSandbox{
		ID:    testSandboxID,
		State: runtimeport.ActualMissing,
	}) {
		t.Fatalf("actual: %#v", actual)
	}
}

// TestRuntimeInspectMapsContainerStates 验证 created/running/stopped 状态和安全元数据。
func TestRuntimeInspectMapsContainerStates(t *testing.T) {
	tests := []struct {
		name       string
		docker     mobycontainer.ContainerState
		wantActual runtimeport.ActualState
	}{
		{
			name:       "created",
			docker:     mobycontainer.StateCreated,
			wantActual: runtimeport.ActualCreated,
		},
		{
			name:       "running",
			docker:     mobycontainer.StateRunning,
			wantActual: runtimeport.ActualRunning,
		},
		{
			name:       "stopped",
			docker:     mobycontainer.StateExited,
			wantActual: runtimeport.ActualStopped,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &fakeEngine{
				containerInspectFunc: func(
					context.Context,
					string,
					mobyclient.ContainerInspectOptions,
				) (mobyclient.ContainerInspectResult, error) {
					result := matchingContainerInspection(
						t,
						testResourceNames(t),
						"container-id",
					)
					result.Container.State = &mobycontainer.State{
						Status:  tt.docker,
						Running: tt.docker == mobycontainer.StateRunning,
						// 原始错误等 Docker 字段不得进入 ActualSandbox。
						Error: "sensitive daemon detail",
					}
					return result, nil
				},
			}

			actual, err := (&Runtime{engine: engine}).Inspect(
				context.Background(),
				testSandboxID,
			)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			want := runtimeport.ActualSandbox{
				ID:                    testSandboxID,
				RuntimeID:             "container-id",
				State:                 tt.wantActual,
				SpecHash:              testSpecHash,
				Workspace:             testWorkspace,
				RunnerProtocolVersion: 1,
			}
			if actual != want {
				t.Fatalf("actual: got %#v, want %#v", actual, want)
			}
		})
	}
}

// TestRuntimeInspectRejectsLabelMismatch 验证同名非受管容器不能伪装成 sandbox。
func TestRuntimeInspectRejectsLabelMismatch(t *testing.T) {
	secret := strings.Repeat("f", 64)
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
						Labels: map[string]string{
							LabelSpecHash: secret,
						},
					},
					State: &mobycontainer.State{Status: mobycontainer.StateRunning},
				},
			}, nil
		},
	}

	_, err := (&Runtime{engine: engine}).Inspect(
		context.Background(),
		testSandboxID,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error: got %v, want conflict", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("inspect conflict exposed label content")
	}
}

// TestRuntimeInspectMapsEngineFailure 验证 inspect Engine 故障保留 cause 且使用安全文案。
func TestRuntimeInspectMapsEngineFailure(t *testing.T) {
	cause := errors.New("inspect daemon secret")
	engine := &fakeEngine{
		containerInspectFunc: func(
			context.Context,
			string,
			mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, cause
		},
	}

	_, err := (&Runtime{engine: engine}).Inspect(
		context.Background(),
		testSandboxID,
	)
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, cause) {
		t.Fatalf("error: got %T %v", err, err)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("inspect error exposed daemon detail")
	}
}
