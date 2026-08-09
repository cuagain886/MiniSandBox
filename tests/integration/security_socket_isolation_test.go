//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestRuntimeSocketsAreIsolatedPerSandbox 验证两个 sandbox 的通信路径、文件身份
// 和删除边界彼此独立，且权限不会允许宿主机其他普通用户直接连接。
func TestRuntimeSocketsAreIsolatedPerSandbox(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	first := createSandbox(t, instance.baseURL, image)
	second := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(first.ID)
	harness.trackSandbox(second.ID)
	waitSandboxState(
		t,
		instance.baseURL,
		first.ID,
		protocol.SandboxStateRunning,
	)
	waitSandboxState(
		t,
		instance.baseURL,
		second.ID,
		protocol.SandboxStateRunning,
	)

	firstSocket := filepath.Join(
		harness.dataDirectory,
		"run",
		first.ID,
		"runner.sock",
	)
	secondSocket := filepath.Join(
		harness.dataDirectory,
		"run",
		second.ID,
		"runner.sock",
	)
	if firstSocket == secondSocket {
		t.Fatal("sandboxes resolved to the same runner socket path")
	}
	firstInfo := assertRuntimeSocketPermissions(t, firstSocket)
	secondInfo := assertRuntimeSocketPermissions(t, secondSocket)
	if os.SameFile(firstInfo, secondInfo) {
		t.Fatal("sandboxes share the same runner socket inode")
	}

	assertRunnerHealthy(t, secondSocket)
	submitSandboxDelete(t, instance.baseURL, first.ID)
	waitSandboxState(
		t,
		instance.baseURL,
		first.ID,
		protocol.SandboxStateTerminated,
	)
	if _, err := os.Lstat(filepath.Dir(firstSocket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleting first sandbox did not remove only its runtime directory")
	}
	assertRuntimeSocketPermissions(t, secondSocket)
	assertRunnerHealthy(t, secondSocket)
}

// assertRuntimeSocketPermissions 验证父目录 0700、socket 0600 并返回文件身份。
func assertRuntimeSocketPermissions(t *testing.T, socketPath string) os.FileInfo {
	t.Helper()
	directoryInfo, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal("stat sandbox runtime directory")
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf(
			"runtime directory mode: got %04o, want 0700",
			directoryInfo.Mode().Perm(),
		)
	}
	socketInfo, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal("stat runner socket")
	}
	if socketInfo.Mode()&os.ModeSocket == 0 ||
		socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf(
			"runner socket mode: got %v/%04o, want socket/0600",
			socketInfo.Mode().Type(),
			socketInfo.Mode().Perm(),
		)
	}
	return socketInfo
}

// assertRunnerHealthy 通过目标 sandbox 的独立 Unix Socket 验证 runner 仍可达。
func assertRunnerHealthy(t *testing.T, socketPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runnerclient.New(socketPath, "").Health(ctx, runnerbootstrap.CurrentProtocolVersion); err != nil {
		t.Fatal("runner socket health failed")
	}
}
