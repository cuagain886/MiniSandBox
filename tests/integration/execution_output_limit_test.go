//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"minisandbox/pkg/protocol"
)

const integrationOutputLimit = 4096

// TestExecutionOutputLimitDrainsProcess 验证 stdout/stderr 共享上限、单次限额事件和超限后进程继续退出。
func TestExecutionOutputLimitDrainsProcess(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		return strings.Replace(content, "runner:\n  execution_uid: 65532\n  execution_gid: 65532", "runner:\n  execution_uid: 65532\n  execution_gid: 65532\n  max_output_bytes: 4096\n  max_log_page_bytes: 4096", 1)
	})
	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	containerID := harness.runningContainerID(t, sandbox.ID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandbox.ID)

	cases := []struct {
		name        string
		stream      string
		bytes       int
		truncated   bool
		limitEvents int
	}{
		{name: "exact-boundary", stream: "stdout", bytes: integrationOutputLimit},
		{name: "stdout", stream: "stdout", bytes: integrationOutputLimit * 4, truncated: true, limitEvents: 1},
		{name: "stderr", stream: "stderr", bytes: integrationOutputLimit * 4, truncated: true, limitEvents: 1},
		{name: "combined", stream: "combined", bytes: integrationOutputLimit * 4, truncated: true, limitEvents: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			marker := "/workspace/p2-output-limit-" + test.name
			events := executeForeground(t, client, protocol.ExecuteRequest{Argv: []string{
				executionHelperPath, "output-limit", test.stream, fmt.Sprint(test.bytes), marker,
			}})
			assertSuccessfulForegroundEvents(t, events)
			if got := len(collectStream(events, protocol.EventStdout)) + len(collectStream(events, protocol.EventStderr)); got != integrationOutputLimit {
				t.Fatalf("retained output bytes: got %d, want %d", got, integrationOutputLimit)
			}
			limitEvents := 0
			for _, event := range events {
				if event.Type == protocol.EventOutputLimitReached {
					limitEvents++
				}
			}
			if limitEvents != test.limitEvents {
				t.Fatalf("output limit events: got %d, want %d", limitEvents, test.limitEvents)
			}
			terminal := events[len(events)-1]
			if terminal.OutputTruncated == nil || *terminal.OutputTruncated != test.truncated {
				t.Fatalf("terminal truncated: got %v, want %v", terminal.OutputTruncated, test.truncated)
			}
			if got := string(copyRegularFile(t, harness.client, containerID, marker)); got != "output-complete" {
				t.Fatalf("completion marker: %q", got)
			}
		})
	}
}
