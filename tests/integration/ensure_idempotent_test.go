//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/application"
	"minisandbox/internal/domain"
	dockerruntime "minisandbox/internal/runtime/docker"
)

// TestRuntimeEnsureRepeatedDoesNotDuplicateResources 验证真实 Docker 幂等复用 ID。
func TestRuntimeEnsureRepeatedDoesNotDuplicateResources(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	if err := os.MkdirAll(
		filepath.Join(harness.dataDirectory, "run"),
		0o700,
	); err != nil {
		t.Fatalf("prepare runtime root: %v", err)
	}
	artifacts, err := dockerruntime.NewEmbeddedArtifactProvider()
	if err != nil {
		t.Fatalf("load embedded integration artifacts: %v", err)
	}
	dockerHost := os.Getenv(dockerHostEnv)
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleTimeout)
	defer cancel()
	runtime, err := dockerruntime.New(
		ctx,
		dockerHost,
		dockerruntime.RuntimeOptions{
			DataDirectory: harness.dataDirectory,
			Artifacts:     artifacts,
			CreateTimeout: time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("create Docker runtime: %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close Docker runtime")
		}
	}()

	id, err := application.NewRandomIDGenerator().NewID()
	if err != nil {
		t.Fatalf("generate sandbox ID: %v", err)
	}
	harness.trackSandbox(id)
	spec := domain.SandboxSpec{
		Image: image,
		Resources: domain.ResourceLimits{
			CPUQuotaMillis: 500,
			MemoryMiB:      256,
			PIDs:           64,
		},
		Workspace: domain.WorkspaceSpec{
			MountPath: domain.WorkspaceMountPath,
		},
		Network: domain.NetworkSpec{Outbound: false},
		Platform: domain.Platform{
			OS:   "linux",
			Arch: "amd64",
		},
	}
	sandbox := domain.Sandbox{
		ID:       id,
		Spec:     spec,
		SpecHash: spec.Hash(),
	}

	first, err := runtime.Ensure(ctx, sandbox)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if first.RuntimeID == "" || first.Workspace == "" {
		t.Fatalf("first actual identity: %#v", first)
	}
	firstVolume, err := harness.client.VolumeInspect(
		ctx,
		first.Workspace,
		mobyclient.VolumeInspectOptions{},
	)
	if err != nil {
		t.Fatalf("inspect first workspace volume")
	}

	for iteration := range 3 {
		actual, err := runtime.Ensure(ctx, sandbox)
		if err != nil {
			t.Fatalf("repeat Ensure %d: %v", iteration, err)
		}
		if actual.RuntimeID != first.RuntimeID ||
			actual.Workspace != firstVolume.Volume.Name {
			t.Fatalf(
				"runtime identity changed: first=%#v repeat=%#v",
				first,
				actual,
			)
		}
	}

	containers, volumes := harness.sandboxResourceCounts(t, id)
	if containers != 1 || volumes != 1 {
		t.Fatalf(
			"duplicate resources: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}
}

// sandboxResourceCounts 按精确 sandbox ID labels 统计两类受管资源。
func (h *dockerHarness) sandboxResourceCounts(
	t *testing.T,
	sandboxID string,
) (int, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	label := dockerruntime.LabelSandboxID + "=" + sandboxID
	containers, err := h.client.ContainerList(
		ctx,
		mobyclient.ContainerListOptions{
			All:     true,
			Filters: make(mobyclient.Filters).Add("label", label),
		},
	)
	if err != nil {
		t.Fatalf("list sandbox containers")
	}
	volumes, err := h.client.VolumeList(
		ctx,
		mobyclient.VolumeListOptions{
			Filters: make(mobyclient.Filters).Add("label", label),
		},
	)
	if err != nil {
		t.Fatalf("list sandbox volumes")
	}
	return len(containers.Items), len(volumes.Items)
}
