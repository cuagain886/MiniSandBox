//go:build integration

package integration

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestDeleteCrashPointMatrix 验证每个删除副作用边界在 SIGKILL 后最终收敛为无资源的 Terminated。
func TestDeleteCrashPointMatrix(t *testing.T) {
	points := []string{
		"delete.desired-cas",
		"delete.runner-shutdown",
		"delete.container-remove",
		"delete.volume-remove",
		"delete.runtime-directory-remove",
		"delete.before-terminated-cas",
		"delete.after-terminated-cas",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			harness := newDockerHarness(t)
			image := integrationImage()
			harness.ensureImage(t, image)
			binary := buildCrashSandboxd(t)
			address := reserveLoopbackAddress(t)
			configPath, key := harness.writeCrashConfig(t, address)
			runRoot := filepath.Join(harness.dataDirectory, "run")
			creator := startCrashSandboxd(t, binary, configPath, address, "", "", key, runRoot)
			waitExternalReady(t, creator)
			sandbox := createSandbox(t, creator.baseURL, image)
			harness.trackSandbox(sandbox.ID)
			waitSandboxState(t, creator.baseURL, sandbox.ID, protocol.SandboxStateRunning)
			creator.stop(t)

			socket := filepath.Join(harness.dataDirectory, "delete-crash.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			crashed := startCrashSandboxd(t, binary, configPath, address, point, socket, key, runRoot)
			waitExternalReady(t, crashed)
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				request, err := http.NewRequest(http.MethodDelete, crashed.baseURL+"/v1/sandboxes/"+sandbox.ID, nil)
				if err != nil {
					return
				}
				response, err := http.DefaultClient.Do(request)
				if err == nil {
					_ = response.Body.Close()
				}
			}()
			waitCrashpoint(t, listener, point)
			crashed.kill(t)
			select {
			case <-requestDone:
			case <-time.After(5 * time.Second):
				t.Fatal("delete request did not unblock after crash")
			}

			restarted := startCrashSandboxd(t, binary, configPath, address, "", "", key, runRoot)
			t.Cleanup(func() {
				if restarted.command.ProcessState == nil {
					restarted.stop(t)
				}
			})
			waitExternalReady(t, restarted)
			// 重复 delete 必须安全；已 Terminated 时 API 可以直接返回 204。
			submitSandboxDelete(t, restarted.baseURL, sandbox.ID)
			waitSandboxState(t, restarted.baseURL, sandbox.ID, protocol.SandboxStateTerminated)
			assertSingleSandboxResources(t, harness, sandbox.ID, 0, 0)
			if _, err := os.Lstat(filepath.Join(runRoot, sandbox.ID)); !os.IsNotExist(err) {
				t.Fatalf("runtime directory remained: %v", err)
			}
		})
	}
}
