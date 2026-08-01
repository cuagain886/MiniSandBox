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
	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestCreateFailureCompensatesAllManagedResources 验证 artifact 注入失败后没有孤儿资源。
func TestCreateFailureCompensatesAllManagedResources(t *testing.T) {
	harness := newDockerHarness(t)
	image := harness.createArtifactFailureImage(t)
	instance := harness.startSandboxd(t)

	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	sandbox = waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateFailed,
	)
	if sandbox.Reason != protocol.SandboxReasonArtifactInjectionFailed {
		t.Fatalf(
			"failure reason: got %s, want %s",
			sandbox.Reason,
			protocol.SandboxReasonArtifactInjectionFailed,
		)
	}

	containers, volumes := harness.sandboxResourceCounts(t, sandbox.ID)
	if containers != 0 || volumes != 0 {
		t.Fatalf(
			"failure compensation left resources: containers=%d volumes=%d",
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
		t.Fatalf("failure compensation left runtime directory: %v", err)
	}
}

// createArtifactFailureImage 构造 `/opt` 为普通文件的镜像，使固定 artifact 解包失败。
//
// 故障镜像只改变容器 rootfs，不替换 runtime、Engine 或补偿实现，因此测试仍
// 经过真实 Docker Copy API 和生产 Reconciler。prep container 使用当前 test
// label，提交出的镜像使用精确 ID 登记，二者都由 harness 清理。
func (h *dockerHarness) createArtifactFailureImage(t *testing.T) string {
	t.Helper()
	image := integrationImage()
	h.ensureImage(t, image)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	prepName := "minisandbox-integration-artifact-prep-" + h.testID
	created, err := h.client.ContainerCreate(
		ctx,
		mobyclient.ContainerCreateOptions{
			Name: prepName,
			Config: &mobycontainer.Config{
				Image:  image,
				Cmd:    []string{"sh", "-c", "rm -rf /opt && : > /opt"},
				Labels: h.labels(),
			},
			HostConfig: &mobycontainer.HostConfig{
				NetworkMode: "none",
			},
		},
	)
	if err != nil {
		t.Fatalf("create artifact failure prep container")
	}
	if _, err := h.client.ContainerStart(
		ctx,
		created.ID,
		mobyclient.ContainerStartOptions{},
	); err != nil {
		t.Fatalf("start artifact failure prep container")
	}
	waitContainerExit(t, ctx, h.client, created.ID)

	reference := "minisandbox-integration-artifact-failure:" + h.testID
	committed, err := h.client.ContainerCommit(
		ctx,
		created.ID,
		mobyclient.ContainerCommitOptions{
			Reference: reference,
			Comment:   "MiniSandbox P1-071 integration fixture",
		},
	)
	if err != nil || committed.ID == "" {
		t.Fatalf("commit artifact failure image")
	}
	h.trackImage(committed.ID)
	if _, err := h.client.ContainerRemove(
		ctx,
		created.ID,
		mobyclient.ContainerRemoveOptions{Force: true},
	); err != nil {
		t.Fatalf("remove artifact failure prep container")
	}
	return reference
}

// waitContainerExit 等待测试准备容器成功退出，避免提交未完成的 rootfs。
func waitContainerExit(
	t *testing.T,
	ctx context.Context,
	client *mobyclient.Client,
	containerID string,
) {
	t.Helper()
	wait := client.ContainerWait(
		ctx,
		containerID,
		mobyclient.ContainerWaitOptions{
			Condition: mobycontainer.WaitConditionNotRunning,
		},
	)
	resultChannel := wait.Result
	errorChannel := wait.Error
	for resultChannel != nil || errorChannel != nil {
		select {
		case result, ok := <-resultChannel:
			if !ok {
				resultChannel = nil
				continue
			}
			if result.StatusCode != 0 || result.Error != nil {
				t.Fatalf("artifact failure prep container exited unsuccessfully")
			}
			return
		case err, ok := <-errorChannel:
			if !ok {
				errorChannel = nil
				continue
			}
			if err != nil {
				t.Fatalf("wait artifact failure prep container")
			}
		case <-ctx.Done():
			t.Fatalf("wait artifact failure prep container timed out")
		}
	}
	t.Fatal("wait artifact failure prep container ended without a result")
}
