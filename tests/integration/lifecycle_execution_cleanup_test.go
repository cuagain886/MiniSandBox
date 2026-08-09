//go:build integration

package integration

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/pkg/protocol"
)

// TestDeleteSandboxCleansExecutionsSidecarAndRuntime 验证删除会收敛活跃任务、半创建 sidecar 及全部 Phase 2 文件。
func TestDeleteSandboxCleansExecutionsSidecarAndRuntime(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandboxID)

	foreground := postPublicForeground(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{Argv: []string{executionHelperPath, "process-tree", "kill"}})
	reader := bufio.NewReader(foreground.Body)
	var foregroundOutput []byte
	for len(parseProcessTreePIDs(foregroundOutput)) != 3 {
		event := readPublicSSEEvent(t, reader)
		if event.Type == protocol.EventStdout {
			foregroundOutput = append(foregroundOutput, collectStream([]protocol.ExecutionEvent{event}, protocol.EventStdout)...)
		}
	}
	background, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "process-tree", "kill"}})
	if err != nil {
		t.Fatalf("start background cleanup fixture: %v", err)
	}
	_ = waitProcessTreePIDs(t, client, background.ExecutionID)
	sidecarID := createHalfCreatedManagedSidecar(t, harness, image, sandboxID)

	if status := submitSandboxDelete(t, instance.baseURL, sandboxID); status != http.StatusAccepted {
		t.Fatalf("delete active sandbox: got %d, want 202", status)
	}
	waitSandboxState(t, instance.baseURL, sandboxID, protocol.SandboxStateTerminated)
	_ = foreground.Body.Close()
	if status := submitSandboxDelete(t, instance.baseURL, sandboxID); status != http.StatusNoContent {
		t.Fatalf("repeat delete: got %d, want 204", status)
	}
	containers, volumes := harness.sandboxResourceCounts(t, sandboxID)
	if containers != 0 || volumes != 0 {
		t.Fatalf("delete left managed resources: containers=%d volumes=%d", containers, volumes)
	}
	for _, id := range []string{containerID, sidecarID} {
		if _, err := harness.client.ContainerInspect(context.Background(), id, mobyclient.ContainerInspectOptions{}); err == nil {
			t.Fatal("deleted sandbox container remains")
		}
	}
	if _, err := os.Lstat(filepath.Join(instance.runRoot, sandboxID)); !os.IsNotExist(err) {
		t.Fatalf("runtime/socket/log directory remains: %v", err)
	}
}

// TestDeleteSandboxWhenRunnerIsUnreachable 验证 runner 自我终止后删除仍由 Docker 边界完成。
func TestDeleteSandboxWhenRunnerIsUnreachable(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	client := instance.runnerClient(t, sandboxID)
	killRunner := `for p in /proc/[0-9]*; do read c < "$p/comm" 2>/dev/null || continue; if [ "$c" = runnerd ]; then kill -KILL "${p#/proc/}"; exit $?; fi; done; exit 42`
	if code := execAndWait(t, harness.client, containerID, "65532:65532", []string{"/bin/sh", "-c", killRunner}); code != 0 {
		t.Fatalf("stop runner fixture: exit=%d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pollCondition(ctx, 25*time.Millisecond, func() (bool, error) {
		_, err := client.Status(ctx, "exec_unreachable")
		return err != nil, nil
	}); err != nil {
		t.Fatalf("runner did not become unreachable: %v", err)
	}
	submitSandboxDelete(t, instance.baseURL, sandboxID)
	waitSandboxState(t, instance.baseURL, sandboxID, protocol.SandboxStateTerminated)
	containers, volumes := harness.sandboxResourceCounts(t, sandboxID)
	if containers != 0 || volumes != 0 {
		t.Fatalf("unreachable runner cleanup: containers=%d volumes=%d", containers, volumes)
	}
}

func createHalfCreatedManagedSidecar(t *testing.T, harness *dockerHarness, image, sandboxID string) string {
	t.Helper()
	labels := map[string]string{
		dockerruntime.LabelManaged:       "true",
		dockerruntime.LabelSchemaVersion: "1",
		dockerruntime.LabelSandboxID:     sandboxID,
		"minisandbox.io/resource-role":   "egress-sidecar",
		testIDLabel:                      harness.testID,
	}
	created, err := harness.client.ContainerCreate(context.Background(), mobyclient.ContainerCreateOptions{
		Name:   "minisandbox-egress-" + sandboxID,
		Config: &mobycontainer.Config{Image: image, Labels: labels},
	})
	if err != nil || created.ID == "" {
		t.Fatalf("create half-created sidecar: %v", err)
	}
	return created.ID
}
