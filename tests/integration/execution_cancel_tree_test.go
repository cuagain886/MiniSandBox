//go:build integration

package integration

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestExplicitCancelTerminatesProcessTree 验证 DELETE 对 TERM 响应、忽略 TERM 的多层后代均执行完整进程组清理。
func TestExplicitCancelTerminatesProcessTree(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandboxID)

	for _, mode := range []string{"term", "kill"} {
		t.Run(mode, func(t *testing.T) {
			descriptor, err := client.ExecuteBackground(context.Background(), protocol.ExecuteRequest{
				Argv: []string{executionHelperPath, "process-tree", mode},
			})
			if err != nil {
				t.Fatalf("start background process tree: %v", err)
			}
			pids := waitProcessTreePIDs(t, client, descriptor.ExecutionID)
			first, err := client.Cancel(context.Background(), descriptor.ExecutionID)
			if err != nil || first != runnerclient.CancelAccepted {
				t.Fatalf("first cancel: disposition=%s err=%v", first, err)
			}
			second, err := client.Cancel(context.Background(), descriptor.ExecutionID)
			if err != nil || second != runnerclient.CancelAccepted && second != runnerclient.CancelAlreadyTerminal {
				t.Fatalf("repeated cancel: disposition=%s err=%v", second, err)
			}
			status := waitExecutionTerminal(t, client, descriptor.ExecutionID)
			assertTerminalState(t, status, protocol.ExecutionStateCancelled, protocol.EventCancelled)
			waitContainerPIDsGone(t, harness, containerID, pids)
			assertSingleStoredTerminal(t, client, descriptor.ExecutionID, protocol.EventCancelled)
		})
	}

	t.Run("cancel_exit_race", func(t *testing.T) {
		descriptor, err := client.ExecuteBackground(context.Background(), protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "process-tree", "race"},
		})
		if err != nil {
			t.Fatalf("start racing process tree: %v", err)
		}
		pids := waitProcessTreePIDs(t, client, descriptor.ExecutionID)
		_, _ = client.Cancel(context.Background(), descriptor.ExecutionID)
		status := waitExecutionTerminal(t, client, descriptor.ExecutionID)
		if status.State != protocol.ExecutionStateCancelled && status.State != protocol.ExecutionStateExited {
			t.Fatalf("cancel/exit race state: %+v", status)
		}
		waitContainerPIDsGone(t, harness, containerID, pids)
		assertSingleStoredTerminal(t, client, descriptor.ExecutionID, status.TerminalEvent.Type)
	})
}

func waitProcessTreePIDs(t *testing.T, client *runnerclient.Client, executionID string) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page, err := client.Logs(context.Background(), executionID, 0)
		if err == nil {
			output := string(collectStream(page.Events, protocol.EventStdout))
			fields := strings.Fields(strings.TrimSpace(output))
			if len(fields) >= 3 {
				pids := make([]int, 0, 3)
				for _, field := range fields[:3] {
					_, value, ok := strings.Cut(field, "=")
					pid, parseErr := strconv.Atoi(value)
					if !ok || parseErr != nil || pid <= 1 {
						pids = nil
						break
					}
					pids = append(pids, pid)
				}
				if len(pids) == 3 {
					return pids
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process tree PIDs were not published")
	return nil
}

func waitExecutionTerminal(t *testing.T, client *runnerclient.Client, executionID string) protocol.ExecutionStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := client.Status(context.Background(), executionID)
		if err == nil && status.TerminalEvent != nil {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution did not reach terminal state")
	return protocol.ExecutionStatus{}
}

func assertTerminalState(t *testing.T, status protocol.ExecutionStatus, state protocol.ExecutionState, eventType protocol.EventType) {
	t.Helper()
	if status.State != state || status.TerminalEvent == nil || status.TerminalEvent.Type != eventType {
		t.Fatalf("execution terminal mismatch: %+v", status)
	}
}

func waitContainerPIDsGone(t *testing.T, harness *dockerHarness, containerID string, pids []int) {
	t.Helper()
	command := ""
	for _, pid := range pids {
		command += fmt.Sprintf("[ ! -e /proc/%d ] || exit 1; ", pid)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/sh", "-c", command}); code == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process tree still exists: %v", pids)
}

func assertSingleStoredTerminal(t *testing.T, client *runnerclient.Client, executionID string, want protocol.EventType) protocol.ExecutionLogPage {
	t.Helper()
	page, err := client.Logs(context.Background(), executionID, 0)
	if err != nil || !page.Complete {
		t.Fatalf("read completed execution log: page=%+v err=%v", page, err)
	}
	terminalCount := 0
	for _, event := range page.Events {
		if event.Terminal() {
			terminalCount++
			if event.Type != want {
				t.Fatalf("stored terminal type: got %s want %s", event.Type, want)
			}
		}
	}
	if terminalCount != 1 {
		t.Fatalf("stored terminal count: %d", terminalCount)
	}
	return page
}
