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
	"minisandbox/internal/domain"
)

// TestEnsureStoppedContainerReusesMatchingContainer 验证受管容器存在时不会重复创建。
func TestEnsureStoppedContainerReusesMatchingContainer(t *testing.T) {
	names := testResourceNames(t)
	createCalls := 0
	engine := &fakeEngine{
		containerInspectFunc: func(
			_ context.Context,
			name string,
			_ mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			if name != names.Container {
				t.Fatalf("inspect name: got %q, want %q", name, names.Container)
			}
			return matchingContainerInspection(t, names, "container-existing"), nil
		},
		containerCreateFunc: func(
			context.Context,
			mobyclient.ContainerCreateOptions,
		) (mobyclient.ContainerCreateResult, error) {
			createCalls++
			return mobyclient.ContainerCreateResult{}, nil
		},
	}

	result, err := ensureStoppedContainer(
		context.Background(),
		engine,
		testDockerSandbox(),
		names,
	)
	if err != nil {
		t.Fatalf("ensure stopped container: %v", err)
	}
	if result.ContainerID != "container-existing" || result.CreatedByThisCall {
		t.Fatalf("result: %#v", result)
	}
	if createCalls != 0 {
		t.Fatalf("create calls: got %d, want 0", createCalls)
	}
}

// TestEnsureStoppedContainerCreatesMissingContainer 验证缺失时仅调用 ContainerCreate。
func TestEnsureStoppedContainerCreatesMissingContainer(t *testing.T) {
	names := testResourceNames(t)
	var captured mobyclient.ContainerCreateOptions
	engine := &fakeEngine{
		containerInspectFunc: func(
			context.Context,
			string,
			mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
		},
		containerCreateFunc: func(
			_ context.Context,
			options mobyclient.ContainerCreateOptions,
		) (mobyclient.ContainerCreateResult, error) {
			captured = options
			return mobyclient.ContainerCreateResult{ID: "container-new"}, nil
		},
	}

	result, err := ensureStoppedContainer(
		context.Background(),
		engine,
		testDockerSandbox(),
		names,
	)
	if err != nil {
		t.Fatalf("ensure stopped container: %v", err)
	}
	if result.ContainerID != "container-new" || !result.CreatedByThisCall {
		t.Fatalf("result: %#v", result)
	}
	expected, err := buildContainerCreateOptions(testDockerSandbox(), names)
	if err != nil {
		t.Fatalf("build expected options: %v", err)
	}
	if !reflect.DeepEqual(captured, expected) {
		t.Fatalf("create options differ:\n got %#v\nwant %#v", captured, expected)
	}
}

// TestEnsureStoppedContainerReportsCreationWhenDaemonOmitsID 验证空 ID 失败仍进入补偿日志。
func TestEnsureStoppedContainerReportsCreationWhenDaemonOmitsID(t *testing.T) {
	engine := &fakeEngine{
		containerInspectFunc: func(
			context.Context,
			string,
			mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
		},
		containerCreateFunc: func(
			context.Context,
			mobyclient.ContainerCreateOptions,
		) (mobyclient.ContainerCreateResult, error) {
			return mobyclient.ContainerCreateResult{}, nil
		},
	}

	result, err := ensureStoppedContainer(
		context.Background(),
		engine,
		testDockerSandbox(),
		testResourceNames(t),
	)
	if err == nil {
		t.Fatal("empty container ID was accepted")
	}
	if !result.CreatedByThisCall {
		t.Fatal("created container was omitted from compensation result")
	}
}

// TestEnsureStoppedContainerRejectsDrift 验证同名非受管和 spec drift 都返回 conflict。
func TestEnsureStoppedContainerRejectsDrift(t *testing.T) {
	names := testResourceNames(t)
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{name: "unmanaged", labels: map[string]string{}},
		{
			name: "spec drift",
			labels: func() map[string]string {
				labels := validTestLabels(t)
				labels[LabelSpecHash] = strings.Repeat("f", 64)
				return labels
			}(),
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
					return mobyclient.ContainerInspectResult{
						Container: mobycontainer.InspectResponse{
							ID:   "container-existing",
							Name: "/" + names.Container,
							Config: &mobycontainer.Config{
								Labels: tt.labels,
							},
						},
					}, nil
				},
			}
			_, err := ensureStoppedContainer(
				context.Background(),
				engine,
				testDockerSandbox(),
				names,
			)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("error: got %v, want conflict", err)
			}
			if strings.Contains(err.Error(), tt.labels[LabelSpecHash]) &&
				tt.labels[LabelSpecHash] != "" {
				t.Fatal("conflict exposed label value")
			}
		})
	}
}

// TestEnsureStoppedContainerMapsEngineErrors 验证 Engine 故障安全映射并保留 cause。
func TestEnsureStoppedContainerMapsEngineErrors(t *testing.T) {
	names := testResourceNames(t)
	inspectCause := errors.New("inspect engine detail")
	createCause := errors.New("create engine detail")
	tests := []struct {
		name       string
		engine     *fakeEngine
		cause      error
		wantCreate bool
	}{
		{
			name: "inspect",
			engine: &fakeEngine{
				containerInspectFunc: func(
					context.Context,
					string,
					mobyclient.ContainerInspectOptions,
				) (mobyclient.ContainerInspectResult, error) {
					return mobyclient.ContainerInspectResult{}, inspectCause
				},
			},
			cause: inspectCause,
		},
		{
			name: "create",
			engine: &fakeEngine{
				containerInspectFunc: func(
					context.Context,
					string,
					mobyclient.ContainerInspectOptions,
				) (mobyclient.ContainerInspectResult, error) {
					return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
				},
				containerCreateFunc: func(
					context.Context,
					mobyclient.ContainerCreateOptions,
				) (mobyclient.ContainerCreateResult, error) {
					return mobyclient.ContainerCreateResult{}, createCause
				},
			},
			cause:      createCause,
			wantCreate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ensureStoppedContainer(
				context.Background(),
				tt.engine,
				testDockerSandbox(),
				names,
			)
			var matched bool
			if tt.wantCreate {
				var createFailed *ContainerCreateFailedError
				matched = errors.As(err, &createFailed)
			} else {
				var unavailable *RuntimeUnavailableError
				matched = errors.As(err, &unavailable)
			}
			if !matched || !errors.Is(err, tt.cause) {
				t.Fatalf("error: got %T %v", err, err)
			}
			if strings.Contains(err.Error(), tt.cause.Error()) {
				t.Fatal("runtime error exposed engine detail")
			}
		})
	}
}

// matchingContainerInspection 构造身份匹配的 Docker inspect 结果。
func matchingContainerInspection(
	t *testing.T,
	names ResourceNames,
	containerID string,
) mobyclient.ContainerInspectResult {
	t.Helper()
	return mobyclient.ContainerInspectResult{
		Container: mobycontainer.InspectResponse{
			ID:   containerID,
			Name: "/" + names.Container,
			Config: &mobycontainer.Config{
				Labels: validTestLabels(t),
			},
		},
	}
}
