package docker

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyimage "github.com/moby/moby/api/types/image"
	mobyvolume "github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

// TestRuntimeEnsureCreatesInFixedOrder 验证 Missing sandbox 的完整原子调用顺序。
func TestRuntimeEnsureCreatesInFixedOrder(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualMissing)
	harness.volumeMissing = true
	runtime := newEnsureRuntime(t, harness.engine)

	actual, err := runtime.Ensure(context.Background(), testDockerSandbox())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if actual.State != runtimeport.ActualRunning ||
		actual.RuntimeID != "container-id" ||
		actual.SpecHash != testSpecHash {
		t.Fatalf("actual: %#v", actual)
	}
	want := []string{
		"container-inspect",
		"image-inspect",
		"volume-inspect",
		"volume-create",
		"container-inspect",
		"container-create",
		"copy-artifacts",
		"container-inspect",
		"container-start",
		"container-inspect",
	}
	if !reflect.DeepEqual(harness.events, want) {
		t.Fatalf("events:\n got %v\nwant %v", harness.events, want)
	}

	// 第二次调用只能 inspect 已 running 容器，不能再创建 volume/container。
	if _, err := runtime.Ensure(
		context.Background(),
		testDockerSandbox(),
	); err != nil {
		t.Fatalf("repeat ensure: %v", err)
	}
	want = append(want, "container-inspect")
	if !reflect.DeepEqual(harness.events, want) {
		t.Fatalf("repeat events:\n got %v\nwant %v", harness.events, want)
	}
}

// TestRuntimeEnsureReusesRunningContainer 验证匹配的 running 容器无额外副作用。
func TestRuntimeEnsureReusesRunningContainer(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualRunning)
	runtime := newEnsureRuntime(t, harness.engine)

	actual, err := runtime.Ensure(context.Background(), testDockerSandbox())
	if err != nil {
		t.Fatalf("ensure running: %v", err)
	}
	if actual.State != runtimeport.ActualRunning {
		t.Fatalf("actual: %#v", actual)
	}
	if !reflect.DeepEqual(harness.events, []string{"container-inspect"}) {
		t.Fatalf("events: %v", harness.events)
	}
}

// TestRuntimeEnsureReinjectsStoppedContainer 验证恢复 stopped 容器时重新注入但不重建。
func TestRuntimeEnsureReinjectsStoppedContainer(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualStopped)
	runtime := newEnsureRuntime(t, harness.engine)

	actual, err := runtime.Ensure(context.Background(), testDockerSandbox())
	if err != nil {
		t.Fatalf("ensure stopped: %v", err)
	}
	if actual.State != runtimeport.ActualRunning {
		t.Fatalf("actual: %#v", actual)
	}
	want := []string{
		"container-inspect",
		"image-inspect",
		"volume-inspect",
		"container-inspect",
		"copy-artifacts",
		"container-inspect",
		"container-start",
		"container-inspect",
	}
	if !reflect.DeepEqual(harness.events, want) {
		t.Fatalf("events:\n got %v\nwant %v", harness.events, want)
	}
}

// TestRuntimeEnsureRejectsSpecDriftBeforeSideEffects 验证已有容器 spec hash 不匹配即停止。
func TestRuntimeEnsureRejectsSpecDriftBeforeSideEffects(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualRunning)
	harness.specHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	runtime := newEnsureRuntime(t, harness.engine)

	_, err := runtime.Ensure(context.Background(), testDockerSandbox())
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error: got %v, want conflict", err)
	}
	if !reflect.DeepEqual(harness.events, []string{"container-inspect"}) {
		t.Fatalf("events: %v", harness.events)
	}
}

// TestRuntimeEnsureStopsAtEachFailedDockerStep 验证每个 Docker 原子错误立即终止后续步骤。
func TestRuntimeEnsureStopsAtEachFailedDockerStep(t *testing.T) {
	for _, step := range []string{
		"initial-inspect",
		"image-inspect",
		"volume-inspect",
		"container-create",
		"copy-artifacts",
		"container-start",
		"final-inspect",
	} {
		t.Run(step, func(t *testing.T) {
			harness := newEnsureHarness(t, runtimeport.ActualMissing)
			harness.failAt = step
			runtime := newEnsureRuntime(t, harness.engine)

			_, err := runtime.Ensure(context.Background(), testDockerSandbox())
			if err == nil || !errors.Is(err, harness.failure) {
				t.Fatalf("error: got %v, want failure cause", err)
			}
		})
	}
}

// TestRuntimeEnsureValidatesBeforeSideEffects 验证 spec/artifact 和 runtime root 错误不被掩盖。
func TestRuntimeEnsureValidatesBeforeSideEffects(t *testing.T) {
	t.Run("invalid spec", func(t *testing.T) {
		harness := newEnsureHarness(t, runtimeport.ActualMissing)
		runtime := newEnsureRuntime(t, harness.engine)
		sandbox := testDockerSandbox()
		sandbox.Spec.Resources.PIDs = 0

		if _, err := runtime.Ensure(context.Background(), sandbox); err == nil {
			t.Fatal("invalid spec was accepted")
		}
		if len(harness.events) != 0 {
			t.Fatalf("Docker calls occurred: %v", harness.events)
		}
	})
	t.Run("invalid artifacts", func(t *testing.T) {
		harness := newEnsureHarness(t, runtimeport.ActualMissing)
		runtime := newEnsureRuntime(t, harness.engine)
		runtime.artifacts = staticArtifactProvider{}

		if _, err := runtime.Ensure(
			context.Background(),
			testDockerSandbox(),
		); err == nil {
			t.Fatal("invalid artifacts were accepted")
		}
		if len(harness.events) != 0 {
			t.Fatalf("Docker calls occurred: %v", harness.events)
		}
	})
	t.Run("runtime root missing", func(t *testing.T) {
		harness := newEnsureHarness(t, runtimeport.ActualMissing)
		runtime := newEnsureRuntime(t, harness.engine)
		runtime.dataDirectory = t.TempDir()

		if _, err := runtime.Ensure(
			context.Background(),
			testDockerSandbox(),
		); err == nil {
			t.Fatal("missing run root was accepted")
		}
		if !reflect.DeepEqual(
			harness.events,
			[]string{"container-inspect"},
		) {
			t.Fatalf("events: %v", harness.events)
		}
	})
}

// ensureHarness 模拟 Ensure 各 Docker 原子的状态变化和调用记录。
type ensureHarness struct {
	t                *testing.T
	engine           *fakeEngine
	events           []string
	initialState     runtimeport.ActualState
	containerChecks  int
	volumeMissing    bool
	volumeCreated    bool
	containerCreated bool
	failAt           string
	failure          error
	specHash         string
}

// newEnsureHarness 创建会从 initialState 收敛到 running 的 Engine fake。
func newEnsureHarness(
	t *testing.T,
	initialState runtimeport.ActualState,
) *ensureHarness {
	t.Helper()
	harness := &ensureHarness{
		t:            t,
		initialState: initialState,
		failure:      errors.New("injected Ensure step failure"),
		specHash:     testSpecHash,
	}
	engine := &fakeEngine{}
	harness.engine = engine
	engine.containerInspectFunc = harness.inspectContainer
	engine.imageInspectFunc = harness.inspectImage
	engine.volumeInspectFunc = harness.inspectVolume
	engine.volumeCreateFunc = harness.createVolume
	engine.containerCreateFunc = harness.createContainer
	engine.copyToContainerFunc = harness.copyArtifacts
	engine.containerStartFunc = harness.startContainer
	engine.containerRemoveFunc = harness.removeContainer
	engine.volumeRemoveFunc = harness.removeVolume
	return harness
}

// inspectContainer 模拟 Missing、stopped 和最终 running 的 inspect 序列。
func (h *ensureHarness) inspectContainer(
	_ context.Context,
	_ string,
	_ mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	h.events = append(h.events, "container-inspect")
	h.containerChecks++
	if h.failAt == "initial-inspect" && h.containerChecks == 1 ||
		h.failAt == "final-inspect" &&
			h.isFinalInspection() {
		return mobyclient.ContainerInspectResult{}, h.failure
	}
	if h.initialState == runtimeport.ActualMissing && h.containerChecks <= 2 {
		return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	state := mobycontainer.StateCreated
	if h.initialState == runtimeport.ActualRunning ||
		h.isFinalInspection() {
		state = mobycontainer.StateRunning
	} else if h.initialState == runtimeport.ActualStopped {
		state = mobycontainer.StateExited
	}
	labels := validTestLabels(h.t)
	labels[LabelSpecHash] = h.specHash
	return mobyclient.ContainerInspectResult{
		Container: mobycontainer.InspectResponse{
			ID:   "container-id",
			Name: "/" + containerName(testSandboxID),
			Config: &mobycontainer.Config{
				Labels: labels,
			},
			State: &mobycontainer.State{
				Status:  state,
				Running: state == mobycontainer.StateRunning,
			},
		},
	}, nil
}

// isFinalInspection 判断当前 inspect 是否位于 start 之后。
func (h *ensureHarness) isFinalInspection() bool {
	switch h.initialState {
	case runtimeport.ActualMissing:
		return h.containerChecks >= 4
	case runtimeport.ActualStopped:
		return h.containerChecks >= 4
	default:
		return false
	}
}

// inspectImage 返回兼容 linux/amd64 镜像。
func (h *ensureHarness) inspectImage(
	context.Context,
	string,
	...mobyclient.ImageInspectOption,
) (mobyclient.ImageInspectResult, error) {
	h.events = append(h.events, "image-inspect")
	if h.failAt == "image-inspect" {
		return mobyclient.ImageInspectResult{}, h.failure
	}
	return mobyclient.ImageInspectResult{
		InspectResponse: mobyimage.InspectResponse{
			Os:           "linux",
			Architecture: "amd64",
		},
	}, nil
}

// inspectVolume 返回匹配 volume，或按测试要求返回 NotFound。
func (h *ensureHarness) inspectVolume(
	context.Context,
	string,
	mobyclient.VolumeInspectOptions,
) (mobyclient.VolumeInspectResult, error) {
	h.events = append(h.events, "volume-inspect")
	if h.failAt == "volume-inspect" {
		return mobyclient.VolumeInspectResult{}, h.failure
	}
	if h.volumeMissing && !h.volumeCreated {
		return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
	}
	return mobyclient.VolumeInspectResult{
		Volume: mobyvolume.Volume{
			Name:   testWorkspace,
			Labels: validTestLabels(h.t),
		},
	}, nil
}

// createVolume 模拟创建受管 workspace volume。
func (h *ensureHarness) createVolume(
	_ context.Context,
	options mobyclient.VolumeCreateOptions,
) (mobyclient.VolumeCreateResult, error) {
	h.events = append(h.events, "volume-create")
	h.volumeCreated = true
	return mobyclient.VolumeCreateResult{
		Volume: mobyvolume.Volume{
			Name:   options.Name,
			Labels: options.Labels,
		},
	}, nil
}

// createContainer 模拟创建 stopped container。
func (h *ensureHarness) createContainer(
	context.Context,
	mobyclient.ContainerCreateOptions,
) (mobyclient.ContainerCreateResult, error) {
	h.events = append(h.events, "container-create")
	if h.failAt == "container-create" {
		return mobyclient.ContainerCreateResult{}, h.failure
	}
	h.containerCreated = true
	return mobyclient.ContainerCreateResult{ID: "container-id"}, nil
}

// copyArtifacts 消费 tar 并模拟 Copy API。
func (h *ensureHarness) copyArtifacts(
	_ context.Context,
	_ string,
	options mobyclient.CopyToContainerOptions,
) (mobyclient.CopyToContainerResult, error) {
	h.events = append(h.events, "copy-artifacts")
	if h.failAt == "copy-artifacts" {
		return mobyclient.CopyToContainerResult{}, h.failure
	}
	if _, err := io.Copy(io.Discard, options.Content); err != nil {
		h.t.Fatalf("consume copy content: %v", err)
	}
	return mobyclient.CopyToContainerResult{}, nil
}

// startContainer 模拟 ContainerStart。
func (h *ensureHarness) startContainer(
	context.Context,
	string,
	mobyclient.ContainerStartOptions,
) (mobyclient.ContainerStartResult, error) {
	h.events = append(h.events, "container-start")
	if h.failAt == "container-start" {
		return mobyclient.ContainerStartResult{}, h.failure
	}
	return mobyclient.ContainerStartResult{}, nil
}

// removeContainer 记录失败补偿删除本次创建的 container。
func (h *ensureHarness) removeContainer(
	context.Context,
	string,
	mobyclient.ContainerRemoveOptions,
) (mobyclient.ContainerRemoveResult, error) {
	h.events = append(h.events, "container-remove")
	h.containerCreated = false
	return mobyclient.ContainerRemoveResult{}, nil
}

// removeVolume 记录失败补偿删除本次创建的 workspace volume。
func (h *ensureHarness) removeVolume(
	context.Context,
	string,
	mobyclient.VolumeRemoveOptions,
) (mobyclient.VolumeRemoveResult, error) {
	h.events = append(h.events, "volume-remove")
	h.volumeCreated = false
	return mobyclient.VolumeRemoveResult{}, nil
}

// newEnsureRuntime 创建带真实临时 runtime root 的测试 Runtime。
func newEnsureRuntime(t *testing.T, engine Engine) *Runtime {
	t.Helper()
	return &Runtime{
		engine:        engine,
		dataDirectory: prepareRuntimeRoot(t),
		artifacts:     testArtifactProvider(),
		createTimeout: time.Second,
	}
}
