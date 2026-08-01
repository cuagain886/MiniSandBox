//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestDeleteSandboxIsIdempotentAndScoped 验证重复 DELETE 不影响其他 sandbox。
func TestDeleteSandboxIsIdempotentAndScoped(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	target := createSandbox(t, instance.baseURL, image)
	survivor := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(target.ID)
	harness.trackSandbox(survivor.ID)
	waitSandboxState(
		t,
		instance.baseURL,
		target.ID,
		protocol.SandboxStateRunning,
	)
	waitSandboxState(
		t,
		instance.baseURL,
		survivor.ID,
		protocol.SandboxStateRunning,
	)

	if status := submitSandboxDelete(
		t,
		instance.baseURL,
		target.ID,
	); status != 202 {
		t.Fatalf("first delete status: got %d, want 202", status)
	}
	waitSandboxState(
		t,
		instance.baseURL,
		target.ID,
		protocol.SandboxStateTerminated,
	)
	if status := submitSandboxDelete(
		t,
		instance.baseURL,
		target.ID,
	); status != 204 {
		t.Fatalf("repeated delete status: got %d, want 204", status)
	}

	waitSandboxState(
		t,
		instance.baseURL,
		survivor.ID,
		protocol.SandboxStateRunning,
	)
	if containerID := harness.runningContainerID(t, survivor.ID); containerID == "" {
		t.Fatal("survivor container disappeared after target deletion")
	}
	targetContainers, targetVolumes := harness.sandboxResourceCounts(t, target.ID)
	if targetContainers != 0 || targetVolumes != 0 {
		t.Fatalf(
			"terminated target left resources: containers=%d volumes=%d",
			targetContainers,
			targetVolumes,
		)
	}
	survivorContainers, survivorVolumes := harness.sandboxResourceCounts(
		t,
		survivor.ID,
	)
	if survivorContainers != 1 || survivorVolumes != 1 {
		t.Fatalf(
			"target deletion crossed sandbox boundary: containers=%d volumes=%d",
			survivorContainers,
			survivorVolumes,
		)
	}
}

// TestDeleteSandboxRecoversFromExternallyRemovedContainer 验证实际资源缺失仍可收敛。
func TestDeleteSandboxRecoversFromExternallyRemovedContainer(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)
	containerID := harness.runningContainerID(t, sandbox.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := harness.client.ContainerRemove(
		ctx,
		containerID,
		mobyclient.ContainerRemoveOptions{Force: true},
	); err != nil {
		t.Fatalf("externally remove sandbox container")
	}

	if status := submitSandboxDelete(
		t,
		instance.baseURL,
		sandbox.ID,
	); status != 202 {
		t.Fatalf("delete after external removal: got %d, want 202", status)
	}
	waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateTerminated,
	)
	containers, volumes := harness.sandboxResourceCounts(t, sandbox.ID)
	if containers != 0 || volumes != 0 {
		t.Fatalf(
			"external removal recovery left resources: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}
}
