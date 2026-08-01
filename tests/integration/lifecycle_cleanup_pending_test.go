//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobymount "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestCleanupPendingCanResumeAfterBlockerRemoval 验证显式 DELETE 可恢复未完成清理。
func TestCleanupPendingCanResumeAfterBlockerRemoval(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	sandbox = waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)
	workspace := harness.workspaceVolumeName(t, sandbox.ID)
	blockerID := harness.startWorkspaceBlocker(t, image, workspace)

	if status := submitSandboxDelete(
		t,
		instance.baseURL,
		sandbox.ID,
	); status != 202 {
		t.Fatalf("first delete status: got %d, want 202", status)
	}
	sandbox = waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateFailed,
	)
	if sandbox.Reason != protocol.SandboxReasonCleanupPending {
		t.Fatalf(
			"failure reason: got %s, want %s",
			sandbox.Reason,
			protocol.SandboxReasonCleanupPending,
		)
	}
	containers, volumes := harness.sandboxResourceCounts(t, sandbox.ID)
	if containers != 0 || volumes != 1 {
		t.Fatalf(
			"partial cleanup state: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := harness.client.ContainerRemove(
		ctx,
		blockerID,
		mobyclient.ContainerRemoveOptions{Force: true},
	); err != nil {
		t.Fatalf("remove workspace blocker")
	}
	if status := submitSandboxDelete(
		t,
		instance.baseURL,
		sandbox.ID,
	); status != 202 {
		t.Fatalf("retry delete status: got %d, want 202", status)
	}
	waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateTerminated,
	)

	containers, volumes = harness.sandboxResourceCounts(t, sandbox.ID)
	if containers != 0 || volumes != 0 {
		t.Fatalf(
			"resumed cleanup left resources: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}
	runtimeDirectory := filepath.Join(
		harness.dataDirectory,
		"run",
		sandbox.ID,
	)
	if _, err := os.Lstat(runtimeDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resumed cleanup left runtime directory: %v", err)
	}
}

// workspaceVolumeName 从目标 sandbox 的真实 container mount 读取 workspace 名称。
func (h *dockerHarness) workspaceVolumeName(
	t *testing.T,
	sandboxID string,
) string {
	t.Helper()
	containerID := h.runningContainerID(t, sandboxID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspection, err := h.client.ContainerInspect(
		ctx,
		containerID,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil {
		t.Fatalf("inspect sandbox workspace mount")
	}
	for _, mount := range inspection.Container.Mounts {
		if mount.Type == mobymount.TypeVolume &&
			mount.Destination == "/workspace" &&
			mount.Name != "" {
			return mount.Name
		}
	}
	t.Fatal("sandbox workspace volume mount not found")
	return ""
}

// startWorkspaceBlocker 启动测试容器占用 workspace，使第一次删除稳定返回冲突。
func (h *dockerHarness) startWorkspaceBlocker(
	t *testing.T,
	image string,
	workspace string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := h.client.ContainerCreate(
		ctx,
		mobyclient.ContainerCreateOptions{
			Name: "minisandbox-integration-volume-blocker-" + h.testID,
			Config: &mobycontainer.Config{
				Image:  image,
				Cmd:    []string{"sleep", "300"},
				Labels: h.labels(),
			},
			HostConfig: &mobycontainer.HostConfig{
				NetworkMode: "none",
				Mounts: []mobymount.Mount{
					{
						Type:   mobymount.TypeVolume,
						Source: workspace,
						Target: "/workspace",
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("create workspace blocker")
	}
	if _, err := h.client.ContainerStart(
		ctx,
		created.ID,
		mobyclient.ContainerStartOptions{},
	); err != nil {
		t.Fatalf("start workspace blocker")
	}
	return created.ID
}
