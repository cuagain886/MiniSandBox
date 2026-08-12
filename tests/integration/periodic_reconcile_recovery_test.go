//go:build integration

package integration

import (
	"path/filepath"
	"testing"

	"minisandbox/pkg/protocol"
)

// TestPeriodicScannerRecoversDroppedCreateAndDeleteWake 验证 Store 已提交而内存通知丢失时周期扫描仍可独立收敛。
func TestPeriodicScannerRecoversDroppedCreateAndDeleteWake(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	binary := buildCrashSandboxd(t)
	address := reserveLoopbackAddress(t)
	configPath, key := harness.writeCrashConfig(t, address)
	runRoot := filepath.Join(harness.dataDirectory, "run")

	creator := startCrashSandboxd(t, binary, configPath, address, "", "", key, runRoot, "MINISANDBOX_TEST_DROP_POINT=wake.create")
	waitExternalReady(t, creator)
	sandbox := createSandbox(t, creator.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, creator.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	assertSingleSandboxResources(t, harness, sandbox.ID, 1, 1)
	creator.stop(t)

	deleter := startCrashSandboxd(t, binary, configPath, address, "", "", key, runRoot, "MINISANDBOX_TEST_DROP_POINT=wake.delete")
	t.Cleanup(func() {
		if deleter.command.ProcessState == nil {
			deleter.stop(t)
		}
	})
	waitExternalReady(t, deleter)
	submitSandboxDelete(t, deleter.baseURL, sandbox.ID)
	waitSandboxState(t, deleter.baseURL, sandbox.ID, protocol.SandboxStateTerminated)
	assertSingleSandboxResources(t, harness, sandbox.ID, 0, 0)
}

func waitExternalReady(t *testing.T, instance *crashSandboxd) {
	t.Helper()
	waitReady(t, &sandboxdInstance{baseURL: instance.baseURL, done: instance.done})
}

func assertSingleSandboxResources(t *testing.T, harness *dockerHarness, sandboxID string, containers, volumes int) {
	t.Helper()
	gotContainers, gotVolumes := harness.sandboxResourceCounts(t, sandboxID)
	if gotContainers != containers || gotVolumes != volumes {
		t.Fatalf("sandbox resources: containers=%d volumes=%d, want %d/%d", gotContainers, gotVolumes, containers, volumes)
	}
}
