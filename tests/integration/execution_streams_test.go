//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestExecutionStreamsPreserveBytesAndSequence 验证 stdout/stderr 分流、Base64 原始字节、连续序号和末尾唯一终态。
func TestExecutionStreamsPreserveBytesAndSequence(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	client := instance.runnerClient(t, sandboxID)

	for _, mode := range []string{"small", "large", "binary", "empty-stderr", "interleaved"} {
		t.Run(mode, func(t *testing.T) {
			wantStdout, wantStderr := streamFixture(mode)
			events := executeForeground(t, client, protocol.ExecuteRequest{Argv: []string{executionHelperPath, "streams", mode}})
			assertSuccessfulForegroundEvents(t, events)
			assertContinuousTerminalStream(t, events)
			if got := collectStream(events, protocol.EventStdout); !bytes.Equal(got, wantStdout) {
				t.Fatalf("stdout mismatch: got=%d bytes want=%d", len(got), len(wantStdout))
			}
			if got := collectStream(events, protocol.EventStderr); !bytes.Equal(got, wantStderr) {
				t.Fatalf("stderr mismatch: got=%d bytes want=%d", len(got), len(wantStderr))
			}
		})
	}
}

func assertContinuousTerminalStream(t *testing.T, events []protocol.ExecutionEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("execution stream is empty")
	}
	executionID := events[0].ExecutionID
	for index, event := range events {
		if event.ExecutionID != executionID || event.Sequence != uint64(index+1) {
			t.Fatalf("event sequence mismatch at %d: %+v", index, event)
		}
		if index < len(events)-1 && event.Terminal() {
			t.Fatalf("terminal event was not last: %+v", event)
		}
	}
	terminal := events[len(events)-1]
	if !terminal.Terminal() || terminal.OutputTruncated == nil || *terminal.OutputTruncated {
		t.Fatalf("terminal output metadata: %+v", terminal)
	}
}

func streamFixture(mode string) ([]byte, []byte) {
	switch mode {
	case "small":
		return []byte("stdout-small"), []byte("stderr-small")
	case "large":
		return bytes.Repeat([]byte("OUT-0123456789abcdef"), 8192), bytes.Repeat([]byte("ERR-fedcba9876543210"), 6144)
	case "binary":
		return []byte{0x00, 0xff, 0x80, 'O', '\n'}, []byte{0xfe, 0x00, 0x81, 'E', '\r', '\n'}
	case "empty-stderr":
		return []byte("stdout-only"), nil
	case "interleaved":
		var stdout, stderr bytes.Buffer
		for index := 0; index < 128; index++ {
			_, _ = fmt.Fprintf(&stdout, "O%03d|", index)
			_, _ = fmt.Fprintf(&stderr, "E%03d|", index)
		}
		return stdout.Bytes(), stderr.Bytes()
	default:
		return nil, nil
	}
}
