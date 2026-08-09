//go:build linux

package runner

import (
	"context"
	"encoding/base64"
	"errors"
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
	launcher, err := NewExecutionLauncher(manager, runnerbootstrap.Config{
		Limits: limits,
		Paths:  runnerbootstrap.Paths{WorkspaceDirectory: t.TempDir()},
	})
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
