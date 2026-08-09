//go:build linux

package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestExecutionLauncherRunsArgv 验证生产组合器真实启动 argv、保留输出并以 exited 收敛。
func TestExecutionLauncherRunsArgv(t *testing.T) {
	launcher := newExecutionLauncherFixture(t)
	handle, err := launcher.StartForeground(context.Background(), ExecutionLaunchRequest{
		Validated: ValidatedRequest{Argv: []string{"/bin/printf", "%s", "hello world"}, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("start foreground: %v", err)
	}
	events := waitLauncherTerminal(t, handle.Events)
	if len(events) != 3 || events[0].Type != protocol.EventStarted || events[1].Type != protocol.EventStdout || events[2].Type != protocol.EventExited {
		t.Fatalf("unexpected events: %+v", events)
	}
	output, err := base64.StdEncoding.DecodeString(events[1].DataBase64)
	if err != nil || string(output) != "hello world" {
		t.Fatalf("stdout: %q, err=%v", output, err)
	}
	if events[2].ExitCode == nil || *events[2].ExitCode != 0 {
		t.Fatalf("terminal exit code: %+v", events[2])
	}
}

// TestExecutionLauncherReleasesSlotAfterStartFailure 验证启动失败形成内部终态并释放并发槽位。
func TestExecutionLauncherReleasesSlotAfterStartFailure(t *testing.T) {
	launcher := newExecutionLauncherFixture(t)
	_, err := launcher.StartForeground(context.Background(), ExecutionLaunchRequest{
		Validated: ValidatedRequest{Argv: []string{"/definitely/missing"}, Timeout: time.Second},
	})
	if !errors.Is(err, ErrProcessStartFailed) {
		t.Fatalf("start missing executable: %v", err)
	}
	handle, err := launcher.StartForeground(context.Background(), ExecutionLaunchRequest{
		Validated: ValidatedRequest{Argv: []string{"/bin/true"}, Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
	_ = waitLauncherTerminal(t, handle.Events)
}

// TestExecutionLauncherBackgroundIgnoresRequestCancellation 验证后台执行只绑定 server lifetime，并在普通请求取消后仍写出完整日志。
func TestExecutionLauncherBackgroundIgnoresRequestCancellation(t *testing.T) {
	launcher := newExecutionLauncherFixture(t)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	descriptor, err := launcher.StartBackground(requestContext, ExecutionLaunchRequest{
		Validated: ValidatedRequest{Argv: []string{"/bin/sh", "-c", "sleep 0.05; printf background-ok"}, Timeout: time.Second, Background: true},
	})
	if err != nil {
		t.Fatalf("start background: %v", err)
	}
	cancelRequest()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot, snapshotErr := launcher.manager.StatusSnapshot(descriptor.ID)
		if snapshotErr != nil {
			t.Fatalf("background status: %v", snapshotErr)
		}
		if snapshot.Descriptor.State == ExecutionExited && snapshot.TerminalEvent != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background did not exit after request cancellation: %+v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
	reader, err := NewBackgroundLogReader(launcher.bootstrap.Paths.ExecutionDataDirectory, 32, 1024*1024)
	if err != nil {
		t.Fatalf("new background reader: %v", err)
	}
	for {
		page, readErr := reader.Read(descriptor.ID, 0)
		if readErr == nil && page.Complete && len(page.Events) >= 3 && page.Events[len(page.Events)-1].Type == protocol.EventExited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background log incomplete: page=%+v err=%v", page, readErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func newExecutionLauncherFixture(t *testing.T) *ExecutionLauncher {
	t.Helper()
	manager, err := NewManager(1)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	limits := runnerbootstrap.Limits{
		DefaultTimeoutNanoseconds: time.Second, MaxTimeoutNanoseconds: time.Minute,
		TerminationGraceNanoseconds: 10 * time.Millisecond, MaxConcurrentExecutions: 1,
		MaxRequestBytes: 64 * 1024, MaxOutputBytes: 1024 * 1024,
		MaxEnvVars: 256, MaxEnvKeyBytes: 256, MaxEnvValueBytes: 4096, MaxEnvTotalBytes: 64 * 1024,
	}
	executionDataDirectory := t.TempDir()
	executionDirectory, err := os.Open(executionDataDirectory)
	if err != nil {
		t.Fatalf("open execution data directory: %v", err)
	}
	t.Cleanup(func() { _ = executionDirectory.Close() })
	launcher, err := NewExecutionLauncher(context.Background(), manager, runnerbootstrap.Config{
		Limits: limits,
		Paths:  runnerbootstrap.Paths{WorkspaceDirectory: t.TempDir(), ExecutionDataDirectory: executionDataDirectory},
	}, executionDirectory)
	if err != nil {
		t.Fatalf("new launcher: %v", err)
	}
	return launcher
}

func waitLauncherTerminal(t *testing.T, store *EventStore) []protocol.ExecutionEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		events, terminal, changed := store.EventsAfter(0)
		if terminal {
			return events
		}
		select {
		case <-ctx.Done():
			t.Fatal("execution did not reach terminal state")
		case <-changed:
		}
	}
}
