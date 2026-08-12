package docker

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"minisandbox/internal/runnerbootstrap"
	runtimeport "minisandbox/internal/runtime"
)

// TestClearComputeRuntimeFilesPreservesLease 验证 compute 替换只清临时文件并保留 lease.json。
func TestClearComputeRuntimeFilesPreservesLease(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dataDirectory, runtimeRootName), 0o700); err != nil {
		t.Fatal(err)
	}
	const sandboxID = "00000000-0000-4000-8000-000000000074"
	paths, err := EnsureRuntimeDirectory(dataDirectory, sandboxID)
	if err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(paths.Directory, runtimeport.LeaseManifestName)
	targets := []string{
		preserved, paths.HostRunnerSocket,
		filepath.Join(paths.Directory, runnerbootstrap.ConfigFileName),
		filepath.Join(paths.Directory, runnerCredentialFileName),
		filepath.Join(paths.Directory, "executions", "execution.json"),
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &Runtime{dataDirectory: dataDirectory}
	if err := runtime.clearComputeRuntimeFiles(sandboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("lease was removed: %v", err)
	}
	for _, target := range targets[1:] {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("temporary target remains: %s err=%v", target, err)
		}
	}
}

// TestRemoveComputeResourcesUsesMainThenSidecarAndPreservesWorkspace 验证替换顺序且不进入 volume 删除路径。
func TestRemoveComputeResourcesUsesMainThenSidecarAndPreservesWorkspace(t *testing.T) {
	runtime, events, _ := newDeleteRuntime(t)
	if err := runtime.removeComputeResources(context.Background(), testSandboxID); err != nil {
		t.Fatal(err)
	}
	want := []string{"container-inspect", "container-remove", "sidecar-inspect"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events: got %v want %v", *events, want)
	}
}
