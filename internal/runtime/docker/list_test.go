package docker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

const secondListSandboxID = "10010203-0405-4607-8809-0a0b0c0d0e0f"

// TestRuntimeListManagedEmptyUsesSafeFilter 验证查询包含 stopped 且只筛受管 label。
func TestRuntimeListManagedEmptyUsesSafeFilter(t *testing.T) {
	engine := &fakeEngine{
		containerListFunc: func(
			_ context.Context,
			options mobyclient.ContainerListOptions,
		) (mobyclient.ContainerListResult, error) {
			if !options.All {
				t.Fatal("managed list excluded stopped containers")
			}
			want := make(mobyclient.Filters).Add(
				"label",
				LabelManaged+"="+labelManagedValue,
			)
			if !reflect.DeepEqual(options.Filters, want) {
				t.Fatalf("filters: got %#v, want %#v", options.Filters, want)
			}
			return mobyclient.ContainerListResult{}, nil
		},
	}

	actual, err := (&Runtime{engine: engine}).ListManaged(context.Background())
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	if len(actual) != 0 {
		t.Fatalf("actual: %#v", actual)
	}
}

// TestRuntimeListManagedMapsAndSortsContainers 验证多状态容器按 sandbox ID 排序。
func TestRuntimeListManagedMapsAndSortsContainers(t *testing.T) {
	engine := &fakeEngine{
		containerListFunc: func(
			context.Context,
			mobyclient.ContainerListOptions,
		) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{
				Items: []mobycontainer.Summary{
					managedSummary(
						t,
						secondListSandboxID,
						"container-b",
						mobycontainer.StateExited,
					),
					managedSummary(
						t,
						testSandboxID,
						"container-a",
						mobycontainer.StateRunning,
					),
				},
			}, nil
		},
	}

	actual, err := (&Runtime{engine: engine}).ListManaged(context.Background())
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	if len(actual) != 2 ||
		actual[0].ID != testSandboxID ||
		actual[0].State != runtimeport.ActualRunning ||
		actual[1].ID != secondListSandboxID ||
		actual[1].State != runtimeport.ActualStopped {
		t.Fatalf("sorted actual: %#v", actual)
	}
}

// TestRuntimeListManagedReturnsDamagedLabelsAsDiagnostic 验证损坏项不阻断合法项。
func TestRuntimeListManagedReturnsDamagedLabelsAsDiagnostic(t *testing.T) {
	damaged := managedSummary(
		t,
		secondListSandboxID,
		"container-damaged",
		mobycontainer.StateRunning,
	)
	delete(damaged.Labels, LabelSpecHash)
	valid := managedSummary(
		t,
		testSandboxID,
		"container-valid",
		mobycontainer.StateCreated,
	)
	engine := &fakeEngine{
		containerListFunc: func(
			context.Context,
			mobyclient.ContainerListOptions,
		) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{
				Items: []mobycontainer.Summary{damaged, valid},
			}, nil
		},
	}

	actual, err := (&Runtime{engine: engine}).ListManaged(context.Background())
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	if len(actual) != 2 ||
		actual[0].DiscoveryIssue != "" ||
		actual[1].ID != secondListSandboxID ||
		actual[1].DiscoveryIssue != runtimeport.DiscoveryLabelsInvalid ||
		actual[1].SpecHash != "" ||
		actual[1].Workspace != "" {
		t.Fatalf("actual diagnostics: %#v", actual)
	}
}

// TestRuntimeListManagedReportsUnknownSchema 验证未知 schema 使用独立稳定诊断。
func TestRuntimeListManagedReportsUnknownSchema(t *testing.T) {
	summary := managedSummary(
		t,
		testSandboxID,
		"container-unknown",
		mobycontainer.StateRunning,
	)
	summary.Labels[LabelSchemaVersion] = "2"
	engine := &fakeEngine{
		containerListFunc: func(
			context.Context,
			mobyclient.ContainerListOptions,
		) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{
				Items: []mobycontainer.Summary{summary},
			}, nil
		},
	}

	actual, err := (&Runtime{engine: engine}).ListManaged(context.Background())
	if err != nil {
		t.Fatalf("list managed: %v", err)
	}
	if len(actual) != 1 ||
		actual[0].ID != testSandboxID ||
		actual[0].DiscoveryIssue != runtimeport.DiscoverySchemaUnsupported {
		t.Fatalf("actual: %#v", actual)
	}
}

// TestRuntimeListManagedMapsEngineError 验证 daemon 故障保留 cause 并安全分类。
func TestRuntimeListManagedMapsEngineError(t *testing.T) {
	cause := errors.New("list daemon secret")
	engine := &fakeEngine{
		containerListFunc: func(
			context.Context,
			mobyclient.ContainerListOptions,
		) (mobyclient.ContainerListResult, error) {
			return mobyclient.ContainerListResult{}, cause
		},
	}

	_, err := (&Runtime{engine: engine}).ListManaged(context.Background())
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, cause) {
		t.Fatalf("error: got %T %v", err, err)
	}
}

// managedSummary 构造身份合法的 Docker list summary。
func managedSummary(
	t *testing.T,
	sandboxID string,
	containerID string,
	state mobycontainer.ContainerState,
) mobycontainer.Summary {
	t.Helper()
	workspace := workspaceName(sandboxID)
	labels, err := EncodeLabels(ManagedLabels{
		SandboxID: sandboxID,
		SpecHash:  testSpecHash,
		Workspace: workspace,
	})
	if err != nil {
		t.Fatalf("encode summary labels: %v", err)
	}
	return mobycontainer.Summary{
		ID:     containerID,
		Names:  []string{"/" + containerName(sandboxID)},
		Labels: labels,
		State:  state,
	}
}
