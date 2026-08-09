//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestExecutionTimeoutTerminatesProcessTree 验证 timeout 与显式取消复用同一 TERM/KILL 进程组清理语义。
func TestExecutionTimeoutTerminatesProcessTree(t *testing.T) {
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
				Argv:           []string{executionHelperPath, "process-tree", mode},
				TimeoutSeconds: 1,
			})
			if err != nil {
				t.Fatalf("start timeout process tree: %v", err)
			}
			pids := waitProcessTreePIDs(t, client, descriptor.ExecutionID)
			status := waitExecutionTerminal(t, client, descriptor.ExecutionID)
			assertTerminalState(t, status, protocol.ExecutionStateTimedOut, protocol.EventTimedOut)
			waitContainerPIDsGone(t, harness, containerID, pids)
			page := assertSingleStoredTerminal(t, client, descriptor.ExecutionID, protocol.EventTimedOut)
			assertTimeoutEventTiming(t, page.Events)
		})
	}

	t.Run("timeout_exit_boundary", func(t *testing.T) {
		descriptor, err := client.ExecuteBackground(context.Background(), protocol.ExecuteRequest{
			Argv:           []string{executionHelperPath, "process-tree", "boundary"},
			TimeoutSeconds: 1,
		})
		if err != nil {
			t.Fatalf("start timeout boundary: %v", err)
		}
		pids := waitProcessTreePIDs(t, client, descriptor.ExecutionID)
		status := waitExecutionTerminal(t, client, descriptor.ExecutionID)
		if status.State != protocol.ExecutionStateTimedOut && status.State != protocol.ExecutionStateExited {
			t.Fatalf("timeout/exit boundary state: %+v", status)
		}
		waitContainerPIDsGone(t, harness, containerID, pids)
		page := assertSingleStoredTerminal(t, client, descriptor.ExecutionID, status.TerminalEvent.Type)
		assertTimeoutEventTiming(t, page.Events)
	})
}

func assertTimeoutEventTiming(t *testing.T, events []protocol.ExecutionEvent) {
	t.Helper()
	if len(events) < 2 {
		t.Fatalf("timeout event stream too short: %+v", events)
	}
	started, terminal := events[0], events[len(events)-1]
	if started.Type != protocol.EventStarted || terminal.Timestamp.Before(started.Timestamp) || terminal.DurationMS == nil || *terminal.DurationMS < 0 {
		t.Fatalf("timeout event timing is invalid: started=%+v terminal=%+v", started, terminal)
	}
	if terminal.Timestamp.Sub(started.Timestamp) > 15*time.Second {
		t.Fatalf("timeout terminal exceeded bounded test window: %s", terminal.Timestamp.Sub(started.Timestamp))
	}
}
