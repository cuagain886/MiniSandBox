package docker

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

// TestRuntimeDeleteCleansAllResourcesInOrder 验证三类资源按固定顺序清理。
func TestRuntimeDeleteCleansAllResourcesInOrder(t *testing.T) {
	runtime, events, directory := newDeleteRuntime(t)

	if err := runtime.Delete(context.Background(), testSandboxID); err != nil {
		t.Fatalf("delete runtime: %v", err)
	}
	want := []string{
		"container-inspect",
		"container-remove",
		"sidecar-inspect",
		"volume-inspect",
		"volume-remove",
	}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events: got %v, want %v", *events, want)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("runtime directory still exists: %v", err)
	}
}

// TestRuntimeDeleteAttemptsEveryStepAndJoinsFailures 验证单次调用保留全部未完成步骤。
func TestRuntimeDeleteAttemptsEveryStepAndJoinsFailures(t *testing.T) {
	runtime, events, _ := newDeleteRuntime(t)
	containerCause := errors.New("container remove failure")
	volumeCause := cerrdefs.ErrConflict
	runtime.engine.(*fakeEngine).containerRemoveFunc = func(
		context.Context,
		string,
		mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error) {
		*events = append(*events, "container-remove")
		return mobyclient.ContainerRemoveResult{}, containerCause
	}
	runtime.engine.(*fakeEngine).volumeRemoveFunc = func(
		context.Context,
		string,
		mobyclient.VolumeRemoveOptions,
	) (mobyclient.VolumeRemoveResult, error) {
		*events = append(*events, "volume-remove")
		return mobyclient.VolumeRemoveResult{}, volumeCause
	}

	err := runtime.Delete(context.Background(), testSandboxID)
	if !errors.Is(err, containerCause) || !errors.Is(err, volumeCause) {
		t.Fatalf("joined error lost causes: %v", err)
	}
	var pending *CleanupPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("joined error lost cleanup pending type: %T %v", err, err)
	}
	want := []string{
		"container-inspect",
		"container-remove",
		"volume-inspect",
		"volume-remove",
	}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events: got %v, want %v", *events, want)
	}
}

// TestRuntimeDeletePartialMissingIsSuccess 验证部分或全部资源缺失仍幂等成功。
func TestRuntimeDeletePartialMissingIsSuccess(t *testing.T) {
	dataDirectory := prepareRuntimeRoot(t)
	engine := &fakeEngine{
		containerInspectFunc: func(
			_ context.Context,
			_ string,
			_ mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
		},
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			return mobyclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
		},
	}
	runtime := &Runtime{engine: engine, dataDirectory: dataDirectory}

	if err := runtime.Delete(context.Background(), testSandboxID); err != nil {
		t.Fatalf("delete partial missing runtime: %v", err)
	}
}

// TestRuntimeDeleteReportsDirectoryFailure 验证最后一步失败时整体不能返回成功。
func TestRuntimeDeleteReportsDirectoryFailure(t *testing.T) {
	runtime, _, directory := newDeleteRuntime(t)
	if err := os.Remove(directory); err != nil {
		t.Fatalf("remove test directory: %v", err)
	}
	if err := os.WriteFile(directory, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("replace directory with file: %v", err)
	}

	if err := runtime.Delete(
		context.Background(),
		testSandboxID,
	); err == nil {
		t.Fatal("directory cleanup failure was hidden")
	}
	if _, err := os.Lstat(directory); err != nil {
		t.Fatalf("unsafe directory replacement was modified: %v", err)
	}
}

// TestRuntimeDeleteSecondCallContinuesFromActualState 验证失败重试不会依赖首次内存进度。
func TestRuntimeDeleteSecondCallContinuesFromActualState(t *testing.T) {
	runtime, _, directory := newDeleteRuntime(t)
	engine := runtime.engine.(*fakeEngine)
	first := true
	engine.containerRemoveFunc = func(
		context.Context,
		string,
		mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error) {
		if first {
			return mobyclient.ContainerRemoveResult{}, errors.New("first remove failed")
		}
		return mobyclient.ContainerRemoveResult{}, nil
	}
	engine.volumeRemoveFunc = func(
		context.Context,
		string,
		mobyclient.VolumeRemoveOptions,
	) (mobyclient.VolumeRemoveResult, error) {
		if first {
			return mobyclient.VolumeRemoveResult{}, cerrdefs.ErrConflict
		}
		return mobyclient.VolumeRemoveResult{}, nil
	}

	if err := runtime.Delete(
		context.Background(),
		testSandboxID,
	); err == nil {
		t.Fatal("first delete unexpectedly succeeded")
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("successful directory step was not retained: %v", err)
	}
	first = false
	if err := runtime.Delete(context.Background(), testSandboxID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// newDeleteRuntime 创建具有 container、volume 和 runtime directory 的测试 Runtime。
func newDeleteRuntime(
	t *testing.T,
) (*Runtime, *[]string, string) {
	t.Helper()
	dataDirectory := prepareRuntimeRoot(t)
	paths, err := EnsureRuntimeDirectory(dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("ensure runtime directory: %v", err)
	}
	events := make([]string, 0, 5)
	engine := &fakeEngine{
		containerInspectFunc: func(
			_ context.Context,
			name string,
			_ mobyclient.ContainerInspectOptions,
		) (mobyclient.ContainerInspectResult, error) {
			if name == egressSidecarName(testSandboxID) {
				events = append(events, "sidecar-inspect")
				return mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
			}
			events = append(events, "container-inspect")
			result := matchingContainerInspection(
				t,
				testResourceNames(t),
				"container-id",
			)
			result.Container.State = &mobycontainer.State{
				Status: mobycontainer.StateExited,
			}
			return result, nil
		},
		containerRemoveFunc: func(
			context.Context,
			string,
			mobyclient.ContainerRemoveOptions,
		) (mobyclient.ContainerRemoveResult, error) {
			events = append(events, "container-remove")
			return mobyclient.ContainerRemoveResult{}, nil
		},
		volumeInspectFunc: func(
			context.Context,
			string,
			mobyclient.VolumeInspectOptions,
		) (mobyclient.VolumeInspectResult, error) {
			events = append(events, "volume-inspect")
			return matchingVolumeInspection(t), nil
		},
		volumeRemoveFunc: func(
			context.Context,
			string,
			mobyclient.VolumeRemoveOptions,
		) (mobyclient.VolumeRemoveResult, error) {
			events = append(events, "volume-remove")
			return mobyclient.VolumeRemoveResult{}, nil
		},
	}
	return &Runtime{
		engine:        engine,
		dataDirectory: dataDirectory,
	}, &events, paths.Directory
}
