//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestExecutionUserCannotConnectRunnerSocket 证明 execution UID 即使知道固定路径也会先被文件权限拒绝，且控制面仍可访问 runner。
func TestExecutionUserCannotConnectRunnerSocket(t *testing.T) {
	const executionIdentity = "65532:65532"
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	containerID := harness.runningContainerID(t, sandbox.ID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))

	socketPath := filepath.Join(harness.dataDirectory, "run", sandbox.ID, "runner.sock")
	assertRuntimeSocketOwner(t, socketPath, uint32(os.Geteuid()), uint32(os.Getegid()))
	for _, token := range []string{"-", "forged-token"} {
		code := execAndWait(t, harness.client, containerID, executionIdentity, []string{
			executionHelperPath, "socket-probe", runnerbootstrap.SocketPath, token,
		})
		if code != 20 {
			t.Fatalf("execution socket probe was not denied by filesystem: exit=%d", code)
		}
	}
	assertRunnerHealthy(t, instance, sandbox.ID)
}

func assertRuntimeSocketOwner(t *testing.T, socketPath string, uid, gid uint32) {
	t.Helper()
	_ = assertRuntimeSocketPermissions(t, socketPath)
	for _, path := range []string{filepath.Dir(socketPath), socketPath} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat managed runner path")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("runner path stat type is unavailable")
		}
		if stat.Uid != uid || stat.Gid != gid {
			t.Fatalf("runner path owner mismatch: uid=%d gid=%d", stat.Uid, stat.Gid)
		}
	}
}
