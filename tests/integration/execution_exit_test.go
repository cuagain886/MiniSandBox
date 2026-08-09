//go:build integration

package integration

import (
	"strconv"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionNonZeroExitRemainsExited 验证业务退出码 0、1、2、127 都映射为 exited，而不是 runner failed。
func TestExecutionNonZeroExitRemainsExited(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandboxID)

	for _, exitCode := range []int{0, 1, 2, 127} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			events := executeForeground(t, client, protocol.ExecuteRequest{
				Argv: []string{executionHelperPath, "exit", strconv.Itoa(exitCode)},
			})
			if len(events) != 2 || events[0].Type != protocol.EventStarted {
				t.Fatalf("execution stream boundaries: %+v", events)
			}
			terminal := events[1]
			if terminal.Type != protocol.EventExited || terminal.ExitCode == nil || *terminal.ExitCode != exitCode {
				t.Fatalf("exit %d terminal: %+v", exitCode, terminal)
			}
			for _, event := range events {
				if event.Type == protocol.EventFailed {
					t.Fatalf("exit %d produced failed: %+v", exitCode, event)
				}
			}
		})
	}
}
