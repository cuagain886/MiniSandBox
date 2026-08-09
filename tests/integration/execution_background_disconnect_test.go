//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestBackgroundClientDisconnectDoesNotCancel 验证普通 POST 客户端关闭后后台命令仍 Exited，runner shutdown 才产生 Cancelled。
func TestBackgroundClientDisconnectDoesNotCancel(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))

	t.Run("ordinary_disconnect_exits", func(t *testing.T) {
		const markerPath = "/workspace/p2-background-disconnect-complete"
		requestContext, cancelRequest := context.WithCancel(context.Background())
		descriptor := postPublicBackground(t, requestContext, instance.baseURL, sandboxID, protocol.ExecuteRequest{
			Argv:       []string{executionHelperPath, "marker", markerPath, "150"},
			Background: true,
		})
		cancelRequest()
		status := waitPublicExecutionTerminal(t, instance.baseURL, sandboxID, descriptor.ExecutionID)
		page := waitPublicExecutionLogs(t, instance.baseURL, sandboxID, descriptor.ExecutionID)
		if status.State != protocol.ExecutionStateExited || status.TerminalEvent == nil || status.TerminalEvent.Type != protocol.EventExited {
			t.Fatalf("background disconnect terminal: status=%+v terminal=%+v events=%+v", status, status.TerminalEvent, page.Events)
		}
		if got := string(copyRegularFile(t, harness.client, containerID, markerPath)); got != "background-complete" {
			t.Fatalf("background completion marker: %q", got)
		}
		if got := string(collectStream(page.Events, protocol.EventStdout)); got != "marker-written" {
			t.Fatalf("background stdout log: %q", got)
		}
	})

	t.Run("runner_shutdown_cancels", func(t *testing.T) {
		client := instance.runnerClient(t, sandboxID)
		descriptor, err := client.ExecuteBackground(context.Background(), protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "process-tree", "kill"},
		})
		if err != nil {
			t.Fatalf("start shutdown control execution: %v", err)
		}
		pids := waitProcessTreePIDs(t, client, descriptor.ExecutionID)
		if err := client.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown runner: %v", err)
		}
		waitContainerPIDsGone(t, harness, containerID, pids)
		events := readStoredExecutionEvents(t, harness, containerID, descriptor.ExecutionID)
		terminalCount := 0
		for _, event := range events {
			if event.Terminal() {
				terminalCount++
				if event.Type != protocol.EventCancelled {
					t.Fatalf("runner shutdown terminal: %+v", event)
				}
			}
		}
		if terminalCount != 1 {
			t.Fatalf("runner shutdown terminal count: %d", terminalCount)
		}
	})
}

func postPublicBackground(t *testing.T, ctx context.Context, baseURL, sandboxID string, request protocol.ExecuteRequest) protocol.ExecutionDescriptor {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode public background request: %v", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create public background request: %v", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("post public background: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("public background response: status=%d body=%s", response.StatusCode, raw)
	}
	var descriptor protocol.ExecutionDescriptor
	if err := json.NewDecoder(response.Body).Decode(&descriptor); err != nil || descriptor.ExecutionID == "" {
		t.Fatalf("decode public background descriptor: %+v err=%v", descriptor, err)
	}
	return descriptor
}

func waitPublicExecutionLogs(t *testing.T, baseURL, sandboxID, executionID string) protocol.ExecutionLogPage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/executions/" + url.PathEscape(executionID) + "/logs")
		if err == nil {
			var page protocol.ExecutionLogPage
			decodeErr := json.NewDecoder(response.Body).Decode(&page)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && page.Complete {
				return page
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("public background logs did not complete")
	return protocol.ExecutionLogPage{}
}

func readStoredExecutionEvents(t *testing.T, harness *dockerHarness, containerID, executionID string) []protocol.ExecutionEvent {
	t.Helper()
	raw := copyRegularFile(t, harness.client, containerID, "/run/minisandbox/executions/"+executionID+".ndjson")
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	events := make([]protocol.ExecutionEvent, 0, len(lines))
	for _, line := range lines {
		var event protocol.ExecutionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Validate() != nil {
			t.Fatalf("decode stored execution event: %v", err)
		}
		events = append(events, event)
	}
	return events
}
