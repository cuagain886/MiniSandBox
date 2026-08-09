//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestExecutionArgvPreservesArgumentBoundaries 验证真实 argv 不经过 shell 展开，并覆盖绝对路径、PATH、空输出与非法请求。
func TestExecutionArgvPreservesArgumentBoundaries(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	installExecutionHelper(t, harness.client, harness.runningContainerID(t, sandbox.ID), buildExecutionHelper(t))
	client := instance.runnerClient(t, sandbox.ID)

	wantArguments := []string{"", "space value", `"quoted"`, "*", "semi;colon", "$HOME", `back\slash`}
	argv := append([]string{executionHelperPath, "argv"}, wantArguments...)
	events := executeForeground(t, client, protocol.ExecuteRequest{Argv: argv})
	assertSuccessfulForegroundEvents(t, events)
	if got := decodeArgumentOutput(t, collectStream(events, protocol.EventStdout)); !equalStrings(got, wantArguments) {
		t.Fatalf("argv boundary mismatch: got %#v, want %#v", got, wantArguments)
	}

	events = executeForeground(t, client, protocol.ExecuteRequest{Argv: []string{"minisandbox-execution-helper", "argv", "path lookup"}})
	assertSuccessfulForegroundEvents(t, events)
	if got := decodeArgumentOutput(t, collectStream(events, protocol.EventStdout)); !equalStrings(got, []string{"path lookup"}) {
		t.Fatalf("PATH argv mismatch: %#v", got)
	}

	events = executeForeground(t, client, protocol.ExecuteRequest{Argv: []string{executionHelperPath, "silent"}})
	assertSuccessfulForegroundEvents(t, events)
	if got := collectStream(events, protocol.EventStdout); len(got) != 0 {
		t.Fatalf("silent argv emitted stdout: %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, invalid := range []protocol.ExecuteRequest{{}, {Argv: []string{""}}} {
		stream, err := client.ExecuteForeground(ctx, invalid)
		if stream != nil {
			_ = stream.Close()
			t.Fatal("invalid argv returned an event stream")
		}
		var status *runnerclient.StatusError
		if !errors.As(err, &status) || status.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid argv status: %v", err)
		}
	}
}

func executeForeground(t *testing.T, client *runnerclient.Client, request protocol.ExecuteRequest) []protocol.ExecutionEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := client.ExecuteForeground(ctx, request)
	if err != nil {
		t.Fatalf("execute foreground: %v", err)
	}
	var events []protocol.ExecutionEvent
	if err := stream.Consume(func(event protocol.ExecutionEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("consume foreground stream: %v", err)
	}
	return events
}

func assertSuccessfulForegroundEvents(t *testing.T, events []protocol.ExecutionEvent) {
	t.Helper()
	if len(events) < 2 || events[0].Type != protocol.EventStarted || events[len(events)-1].Type != protocol.EventExited {
		t.Fatalf("foreground boundaries: %+v", events)
	}
	terminalCount := 0
	for _, event := range events {
		if event.Terminal() {
			terminalCount++
		}
	}
	if terminalCount != 1 || events[len(events)-1].ExitCode == nil || *events[len(events)-1].ExitCode != 0 {
		t.Fatalf("foreground terminal: %+v", events[len(events)-1])
	}
}

func collectStream(events []protocol.ExecutionEvent, eventType protocol.EventType) []byte {
	var result []byte
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
		if err != nil {
			return nil
		}
		result = append(result, decoded...)
	}
	return result
}

func decodeArgumentOutput(t *testing.T, output []byte) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		lengthText, encoded, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("invalid argv helper line: %q", line)
		}
		length, err := strconv.Atoi(lengthText)
		decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if err != nil || decodeErr != nil || len(decoded) != length {
			t.Fatalf("invalid argv helper payload: %q", line)
		}
		result = append(result, string(decoded))
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
