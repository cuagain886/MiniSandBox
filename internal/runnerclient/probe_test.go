package runnerclient

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
)

const (
	testProbeSandboxID = "00010203-0405-4607-8809-0a0b0c0d0e0f"
	testProbeToken     = "test-runner-token"
)

// TestNewRunnerProbeValidatesConfiguration 验证 adapter 不接受相对根目录或无界 timeout。
func TestNewRunnerProbeValidatesConfiguration(t *testing.T) {
	if _, err := NewRunnerProbe("relative/run", time.Second, testProbeToken); err == nil {
		t.Fatal("relative socket root was accepted")
	}
	if _, err := NewRunnerProbe(t.TempDir(), 0, testProbeToken); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if _, err := NewRunnerProbe(t.TempDir(), time.Second, " "); err == nil {
		t.Fatal("blank token was accepted")
	}
}

// TestRunnerProbeRejectsPathTraversal 验证接口输入不能替换 socket path。
func TestRunnerProbeRejectsPathTraversal(t *testing.T) {
	probe, err := NewRunnerProbe(t.TempDir(), time.Second, testProbeToken)
	if err != nil {
		t.Fatalf("new runner probe: %v", err)
	}
	for _, sandboxID := range []string{
		"../sandbox",
		testProbeSandboxID + "/other",
		testProbeSandboxID + `\other`,
		"",
	} {
		if err := probe.Probe(context.Background(), sandboxID, runnerbootstrap.CurrentProtocolVersion); err == nil {
			t.Fatalf("unsafe sandbox ID accepted: %q", sandboxID)
		}
	}
}

// TestRunnerProbeBuildsOnlyManagedSocketPath 验证 socket 位于固定 ID 子目录。
func TestRunnerProbeBuildsOnlyManagedSocketPath(t *testing.T) {
	root := t.TempDir()
	probe, err := NewRunnerProbe(root, time.Second, testProbeToken)
	if err != nil {
		t.Fatalf("new runner probe: %v", err)
	}
	socketPath, err := probe.socketPath(testProbeSandboxID)
	if err != nil {
		t.Fatalf("build socket path: %v", err)
	}
	want := filepath.Join(root, testProbeSandboxID, runnerSocketName)
	if socketPath != want {
		t.Fatalf("socket path: got %q, want %q", socketPath, want)
	}
}
