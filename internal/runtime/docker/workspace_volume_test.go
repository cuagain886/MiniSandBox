package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
)

// TestEnsureWorkspaceVolumeReusesMatchingVolume 验证重复调用只复用身份完全匹配的资源。
func TestEnsureWorkspaceVolumeReusesMatchingVolume(t *testing.T) {
	createCalls := 0
	engine := &fakeEngine{
		volumeInspectFunc: func(
			_ context.Context,
			name string,
			_ mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			if name != testWorkspace {
				t.Fatalf("inspect name: got %q, want %q", name, testWorkspace)
			}
			return mobyclient.VolumeInspectResult{
				Volume: mobyvolume.Volume{
					Name:   testWorkspace,
					Labels: validTestLabels(t),
				},
			}, nil
		},
		volumeCreateFunc: func(
			context.Context,
			mobyclient.VolumeCreateOptions,
		) (mobyclient.VolumeCreateResult, error) {
			createCalls++
			return mobyclient.VolumeCreateResult{}, nil
		},
	}

	result, err := ensureWorkspaceVolume(
		context.Background(),
		engine,
		testSandboxID,
		testSpecHash,
	)
	if err != nil {
		t.Fatalf("ensure workspace volume: %v", err)
	}
	if result.Name != testWorkspace || result.CreatedByThisCall {
		t.Fatalf("result: %#v", result)
	}
	if createCalls != 0 {
		t.Fatalf("create calls: got %d, want 0", createCalls)
	}
}

// TestEnsureWorkspaceVolumeCreatesMissingVolume 验证创建请求只携带确定性名称和安全恢复 labels。
func TestEnsureWorkspaceVolumeCreatesMissingVolume(t *testing.T) {
	var captured mobyclient.VolumeCreateOptions
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
		},
		volumeCreateFunc: func(
			_ context.Context,
			options mobyclient.VolumeCreateOptions,
		) (mobyclient.VolumeCreateResult, error) {
			captured = options
			return mobyclient.VolumeCreateResult{
				Volume: mobyvolume.Volume{
					Name:   options.Name,
					Labels: options.Labels,
				},
			}, nil
		},
	}

	result, err := ensureWorkspaceVolume(
		context.Background(),
		engine,
		testSandboxID,
		testSpecHash,
	)
	if err != nil {
		t.Fatalf("ensure workspace volume: %v", err)
	}
	if result.Name != testWorkspace || !result.CreatedByThisCall {
		t.Fatalf("result: %#v", result)
	}
	if captured.Name != testWorkspace {
		t.Fatalf("create name: got %q, want %q", captured.Name, testWorkspace)
	}
	if !reflect.DeepEqual(captured.Labels, validTestLabels(t)) {
		t.Fatalf("create labels: %#v", captured.Labels)
	}
	if captured.Driver != "" ||
		captured.DriverOpts != nil ||
		captured.ClusterVolumeSpec != nil {
		t.Fatalf("unexpected volume options: %#v", captured)
	}
}

// TestEnsureWorkspaceVolumeRejectsIdentityConflicts 验证同名非受管资源和旧规格资源都不会被接管。
func TestEnsureWorkspaceVolumeRejectsIdentityConflicts(t *testing.T) {
	differentSpecHash := strings.Repeat("f", 64)
	tests := []struct {
		name   string
		volume mobyvolume.Volume
	}{
		{
			name: "unmanaged volume",
			volume: mobyvolume.Volume{
				Name:   testWorkspace,
				Labels: map[string]string{},
			},
		},
		{
			name: "different spec hash",
			volume: mobyvolume.Volume{
				Name: testWorkspace,
				Labels: func() map[string]string {
					labels := validTestLabels(t)
					labels[LabelSpecHash] = differentSpecHash
					return labels
				}(),
			},
		},
		{
			name: "different returned name",
			volume: mobyvolume.Volume{
				Name:   "someone-else",
				Labels: validTestLabels(t),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createCalls := 0
			engine := &fakeEngine{
				volumeInspectFunc: func(
					context.Context,
					string,
					mobyclient.VolumeInspectOptions,
				) (mobyclient.VolumeInspectResult, error) {
					return mobyclient.VolumeInspectResult{Volume: tt.volume}, nil
				},
				volumeCreateFunc: func(
					context.Context,
					mobyclient.VolumeCreateOptions,
				) (mobyclient.VolumeCreateResult, error) {
					createCalls++
					return mobyclient.VolumeCreateResult{}, nil
				},
			}

			_, err := ensureWorkspaceVolume(
				context.Background(),
				engine,
				testSandboxID,
				testSpecHash,
			)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("error: got %v, want conflict", err)
			}
			if strings.Contains(err.Error(), differentSpecHash) {
				t.Fatal("conflict error exposed volume metadata")
			}
			if createCalls != 0 {
				t.Fatalf("create calls: got %d, want 0", createCalls)
			}
		})
	}
}

// TestEnsureWorkspaceVolumeValidatesCreateResult 验证创建竞态返回的异主资源仍会被拒绝。
func TestEnsureWorkspaceVolumeValidatesCreateResult(t *testing.T) {
	engine := &fakeEngine{
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
		},
		volumeCreateFunc: func(
			context.Context,
			mobyclient.VolumeCreateOptions,
		) (mobyclient.VolumeCreateResult, error) {
			return mobyclient.VolumeCreateResult{
				Volume: mobyvolume.Volume{
					Name:   testWorkspace,
					Labels: map[string]string{},
				},
			}, nil
		},
	}

	result, err := ensureWorkspaceVolume(
		context.Background(),
		engine,
		testSandboxID,
		testSpecHash,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error: got %v, want conflict", err)
	}
	if !result.CreatedByThisCall {
		t.Fatal("created volume was omitted from compensation result")
	}
}

// TestEnsureWorkspaceVolumeMapsEngineFailures 验证 inspect/create 故障保留 cause 且统一标记运行时不可用。
func TestEnsureWorkspaceVolumeMapsEngineFailures(t *testing.T) {
	inspectCause := errors.New("inspect socket detail")
	createCause := errors.New("create driver detail")
	tests := []struct {
		name   string
		engine *fakeEngine
		cause  error
	}{
		{
			name: "inspect",
			engine: &fakeEngine{
				volumeInspectFunc: func(
					context.Context,
					string,
					mobyclient.VolumeInspectOptions,
				) (mobyclient.VolumeInspectResult, error) {
					return mobyclient.VolumeInspectResult{}, inspectCause
				},
			},
			cause: inspectCause,
		},
		{
			name: "create",
			engine: &fakeEngine{
				volumeInspectFunc: func(
					context.Context,
					string,
					mobyclient.VolumeInspectOptions,
				) (mobyclient.VolumeInspectResult, error) {
					return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
				},
				volumeCreateFunc: func(
					context.Context,
					mobyclient.VolumeCreateOptions,
				) (mobyclient.VolumeCreateResult, error) {
					return mobyclient.VolumeCreateResult{}, createCause
				},
			},
			cause: createCause,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ensureWorkspaceVolume(
				context.Background(),
				tt.engine,
				testSandboxID,
				testSpecHash,
			)
			var unavailable *RuntimeUnavailableError
			if !errors.As(err, &unavailable) || !errors.Is(err, tt.cause) {
				t.Fatalf("error: got %T %v, want unavailable with cause", err, err)
			}
			if strings.Contains(err.Error(), tt.cause.Error()) {
				t.Fatal("runtime error exposed engine detail")
			}
		})
	}
}
