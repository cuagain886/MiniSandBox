//go:build integration

package integration

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestCompletedExecutionGCRetention 验证数量/时间驱逐、运行中保护和日志目录最终有界。
func TestCompletedExecutionGCRetention(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		harness, instance, sandboxID, containerID, client := startGCExecutionSandbox(t, "1h", 2)
		running, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "process-tree", "kill"}})
		if err != nil {
			t.Fatalf("start running execution: %v", err)
		}
		_ = waitProcessTreePIDs(t, client, running.ExecutionID)

		completed := make([]protocol.ExecutionDescriptor, 4)
		for index := range completed {
			completed[index], err = client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "exit", "0"}})
			if err != nil {
				t.Fatalf("start completed execution %d: %v", index, err)
			}
			_ = waitExecutionTerminal(t, client, completed[index].ExecutionID)
		}
		waitRunnerExecutionsNotFound(t, client, completed[0].ExecutionID, completed[1].ExecutionID)
		for _, descriptor := range completed[2:] {
			if status, err := client.Status(t.Context(), descriptor.ExecutionID); err != nil || status.State != protocol.ExecutionStateExited {
				t.Fatalf("retained execution status: id=%s status=%+v err=%v", descriptor.ExecutionID, status, err)
			}
		}
		if status, err := client.Status(t.Context(), running.ExecutionID); err != nil || status.State != protocol.ExecutionStateRunning {
			t.Fatalf("running execution was evicted: status=%+v err=%v", status, err)
		}
		if got := countExecutionLogFiles(t, harness.client, containerID); got > 3 {
			t.Fatalf("execution log directory is not bounded: got %d, want <= 3", got)
		}
		if _, err := client.Cancel(t.Context(), running.ExecutionID); err != nil {
			t.Fatalf("cancel protected execution: %v", err)
		}
		_ = instance
		_ = sandboxID
	})

	t.Run("time", func(t *testing.T) {
		_, _, _, _, client := startGCExecutionSandbox(t, "1s", 100)
		running, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "process-tree", "kill"}})
		if err != nil {
			t.Fatalf("start running execution: %v", err)
		}
		_ = waitProcessTreePIDs(t, client, running.ExecutionID)
		completed, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "exit", "0"}})
		if err != nil {
			t.Fatalf("start expiring execution: %v", err)
		}
		_ = waitExecutionTerminal(t, client, completed.ExecutionID)
		waitRunnerExecutionsNotFound(t, client, completed.ExecutionID)
		if status, err := client.Status(t.Context(), running.ExecutionID); err != nil || status.State != protocol.ExecutionStateRunning {
			t.Fatalf("running execution expired: status=%+v err=%v", status, err)
		}
		if _, err := client.Cancel(t.Context(), running.ExecutionID); err != nil {
			t.Fatalf("cancel protected execution: %v", err)
		}
	})
}

func startGCExecutionSandbox(t *testing.T, retention string, maxRetained int) (*dockerHarness, *sandboxdInstance, string, string, *runnerclient.Client) {
	t.Helper()
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		overrides := "runner:\n  execution_uid: 65532\n  execution_gid: 65532\n  completed_retention: \"" + retention + "\"\n  max_retained_executions: " + fmt.Sprint(maxRetained)
		return strings.Replace(content, "runner:\n  execution_uid: 65532\n  execution_gid: 65532", overrides, 1)
	})
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	return harness, instance, sandboxID, containerID, instance.runnerClient(t, sandboxID)
}

func waitRunnerExecutionsNotFound(t *testing.T, client *runnerclient.Client, executionIDs ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allMissing := true
		for _, executionID := range executionIDs {
			_, err := client.Status(t.Context(), executionID)
			var status *runnerclient.StatusError
			if !errors.As(err, &status) || status.StatusCode != http.StatusNotFound {
				allMissing = false
				break
			}
		}
		if allMissing {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("completed executions remained queryable: %v", executionIDs)
}

func countExecutionLogFiles(t *testing.T, client *mobyclient.Client, containerID string) int {
	t.Helper()
	result, err := client.CopyFromContainer(context.Background(), containerID, mobyclient.CopyFromContainerOptions{SourcePath: "/run/minisandbox/executions"})
	if err != nil {
		t.Fatalf("copy execution log directory: %v", err)
	}
	defer result.Content.Close()
	reader := tar.NewReader(result.Content)
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return count
		}
		if err != nil {
			t.Fatalf("read execution log directory: %v", err)
		}
		if header.Typeflag == tar.TypeReg && filepath.Ext(header.Name) == ".ndjson" {
			count++
		}
	}
}
