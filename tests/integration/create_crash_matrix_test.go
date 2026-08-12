//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/pkg/protocol"
)

// TestCreateCrashPointMatrix 验证每个创建副作用边界在 SIGKILL 后只依赖持久事实恢复。
func TestCreateCrashPointMatrix(t *testing.T) {
	points := []string{
		"create.store-commit",
		"create.runtime-directory",
		"create.workspace-volume",
		"create.container",
		"create.artifact-copy",
		"create.container-start",
		"create.runner-ready",
		"create.before-running-cas",
		"create.after-running-cas",
	}
	for round := 1; round <= 2; round++ {
		for _, point := range points {
			t.Run(point+"-round-"+string(rune('0'+round)), func(t *testing.T) {
				harness := newDockerHarness(t)
				image := integrationImage()
				harness.ensureImage(t, image)
				binary := buildCrashSandboxd(t)
				address := reserveLoopbackAddress(t)
				configPath, key := harness.writeCrashConfig(t, address)
				socket := filepath.Join(harness.dataDirectory, "create-crash.sock")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				crashed := startCrashSandboxd(t, binary, configPath, address, point, socket, key, filepath.Join(harness.dataDirectory, "run"))
				waitExternalReady(t, crashed)
				body, _ := json.Marshal(protocol.CreateSandboxRequest{Image: image})
				requestDone := make(chan struct{})
				go func() {
					defer close(requestDone)
					response, err := http.Post(crashed.baseURL+"/v1/sandboxes", "application/json", bytes.NewReader(body))
					if err == nil {
						_ = response.Body.Close()
					}
				}()
				waitCrashpoint(t, listener, point)
				crashed.kill(t)
				select {
				case <-requestDone:
				case <-time.After(5 * time.Second):
					t.Fatal("create request did not unblock after crash")
				}
				store, err := sqlitestore.Open(filepath.Join(harness.dataDirectory, "sandboxd.db"))
				if err != nil {
					t.Fatal(err)
				}
				records, err := store.ListAll(t.Context())
				_ = store.Close()
				if err != nil || len(records) != 1 {
					t.Fatalf("created records=%d err=%v", len(records), err)
				}
				sandboxID := records[0].ID
				harness.trackSandbox(sandboxID)
				restarted := startCrashSandboxd(t, binary, configPath, address, "", "", key, filepath.Join(harness.dataDirectory, "run"))
				t.Cleanup(func() {
					if restarted.command.ProcessState == nil {
						restarted.stop(t)
					}
				})
				waitExternalReady(t, restarted)
				waitSandboxState(t, restarted.baseURL, sandboxID, protocol.SandboxStateRunning)
				assertSingleSandboxResources(t, harness, sandboxID, 1, 1)
			})
		}
	}
}
