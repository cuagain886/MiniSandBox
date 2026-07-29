package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	mobyclient "github.com/moby/moby/client"
	runtimeport "minisandbox/internal/runtime"
)

// TestRuntimeEnsureCompensatesNewResourcesAtEveryFailureStage 验证各阶段失败只逆序清理本次副作用。
func TestRuntimeEnsureCompensatesNewResourcesAtEveryFailureStage(t *testing.T) {
	tests := []struct {
		name       string
		failAt     string
		wantSuffix []string
	}{
		{
			name:       "image",
			failAt:     "image-inspect",
			wantSuffix: []string{"image-inspect"},
		},
		{
			name:       "volume",
			failAt:     "volume-inspect",
			wantSuffix: []string{"volume-inspect"},
		},
		{
			name:   "container create",
			failAt: "container-create",
			wantSuffix: []string{
				"container-create",
				"volume-inspect",
				"volume-remove",
			},
		},
		{
			name:   "artifact copy",
			failAt: "copy-artifacts",
			wantSuffix: []string{
				"copy-artifacts",
				"container-inspect",
				"container-remove",
				"volume-inspect",
				"volume-remove",
			},
		},
		{
			name:   "container start",
			failAt: "container-start",
			wantSuffix: []string{
				"container-start",
				"container-inspect",
				"container-remove",
				"volume-inspect",
				"volume-remove",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newEnsureHarness(t, runtimeport.ActualMissing)
			harness.volumeMissing = true
			harness.failAt = tt.failAt
			runtime := newEnsureRuntime(t, harness.engine)

			_, err := runtime.Ensure(
				context.Background(),
				testDockerSandbox(),
			)
			if err == nil || !errors.Is(err, harness.failure) {
				t.Fatalf("ensure error: %v", err)
			}
			if !hasEventSuffix(harness.events, tt.wantSuffix) {
				t.Fatalf(
					"events do not end in compensation order:\n got %v\nwant suffix %v",
					harness.events,
					tt.wantSuffix,
				)
			}
			if _, statErr := os.Lstat(
				filepath.Join(runtime.dataDirectory, "run", testSandboxID),
			); !os.IsNotExist(statErr) {
				t.Fatalf("runtime directory was not compensated: %v", statErr)
			}
		})
	}
}

// TestRuntimeEnsureDoesNotCompensateReusedResources 验证失败不会删除调用前已有资源。
func TestRuntimeEnsureDoesNotCompensateReusedResources(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualStopped)
	harness.failAt = "copy-artifacts"
	runtime := newEnsureRuntime(t, harness.engine)
	paths, err := EnsureRuntimeDirectory(runtime.dataDirectory, testSandboxID)
	if err != nil {
		t.Fatalf("prepare existing runtime directory: %v", err)
	}

	_, err = runtime.Ensure(context.Background(), testDockerSandbox())
	if err == nil {
		t.Fatal("ensure unexpectedly succeeded")
	}
	for _, event := range harness.events {
		if event == "container-remove" || event == "volume-remove" {
			t.Fatalf("reused resource was removed: %v", harness.events)
		}
	}
	if _, err := os.Lstat(paths.Directory); err != nil {
		t.Fatalf("reused runtime directory was removed: %v", err)
	}
}

// TestRuntimeEnsureReturnsCleanupPendingAndContinuesCompensation 验证补偿失败仍继续后续步骤。
func TestRuntimeEnsureReturnsCleanupPendingAndContinuesCompensation(t *testing.T) {
	harness := newEnsureHarness(t, runtimeport.ActualMissing)
	harness.volumeMissing = true
	harness.failAt = "copy-artifacts"
	cleanupCause := errors.New("remove container failed")
	harness.engine.containerRemoveFunc = func(
		context.Context,
		string,
		mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error) {
		harness.events = append(harness.events, "container-remove")
		return mobyclient.ContainerRemoveResult{}, cleanupCause
	}
	runtime := newEnsureRuntime(t, harness.engine)

	_, err := runtime.Ensure(context.Background(), testDockerSandbox())
	var pending *CleanupPendingError
	if !errors.As(err, &pending) ||
		!errors.Is(err, harness.failure) ||
		!errors.Is(err, cleanupCause) {
		t.Fatalf("cleanup pending error lost causes: %T %v", err, err)
	}
	if !hasEventSuffix(harness.events, []string{
		"container-remove",
		"volume-inspect",
		"volume-remove",
	}) {
		t.Fatalf("compensation stopped after first failure: %v", harness.events)
	}
	if _, statErr := os.Lstat(
		filepath.Join(runtime.dataDirectory, "run", testSandboxID),
	); !os.IsNotExist(statErr) {
		t.Fatalf("directory compensation did not continue: %v", statErr)
	}
}

// hasEventSuffix 判断事件序列是否以指定补偿序列结束。
func hasEventSuffix(events []string, suffix []string) bool {
	if len(events) < len(suffix) {
		return false
	}
	return reflect.DeepEqual(events[len(events)-len(suffix):], suffix)
}
