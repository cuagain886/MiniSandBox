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

// TestIdempotencyReplaysCommittedCreateAfterLostResponse 验证 sandbox 与响应已经原子提交、
// 但进程在发送 HTTP 响应前被强杀时，重启后的同 key 请求仍逐字节命中原始资源。
func TestIdempotencyReplaysCommittedCreateAfterLostResponse(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	binary := buildCrashSandboxd(t)
	address := reserveLoopbackAddress(t)
	configPath, key := harness.writeCrashConfig(t, address)
	socket := filepath.Join(harness.dataDirectory, "idempotency-lost-response.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	crashed := startCrashSandboxd(t, binary, configPath, address, "create.store-commit", socket, key, filepath.Join(harness.dataDirectory, "run"))
	waitExternalReady(t, crashed)
	body, err := json.Marshal(protocol.CreateSandboxRequest{Image: image})
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		request, _ := http.NewRequest(http.MethodPost, crashed.baseURL+"/v1/sandboxes", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "lost-response-key")
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	waitCrashpoint(t, listener, "create.store-commit")
	crashed.kill(t)
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lost-response request did not unblock")
	}

	store, err := sqlitestore.Open(filepath.Join(harness.dataDirectory, "sandboxd.db"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.ListAll(t.Context())
	_ = store.Close()
	if err != nil || len(records) != 1 {
		t.Fatalf("committed records=%d err=%v", len(records), err)
	}
	originalID := records[0].ID
	harness.trackSandbox(originalID)

	restarted := startCrashSandboxd(t, binary, configPath, address, "", "", key, filepath.Join(harness.dataDirectory, "run"))
	t.Cleanup(func() {
		if restarted.command.ProcessState == nil {
			restarted.stop(t)
		}
	})
	waitExternalReady(t, restarted)
	request, err := http.NewRequest(http.MethodPost, restarted.baseURL+"/v1/sandboxes", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "lost-response-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var replay protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || replay.ID != originalID ||
		response.Header.Get("Location") != "/v1/sandboxes/"+originalID {
		t.Fatalf("replay: status=%d id=%q location=%q want=%q", response.StatusCode, replay.ID, response.Header.Get("Location"), originalID)
	}
	waitSandboxState(t, restarted.baseURL, originalID, protocol.SandboxStateRunning)
	assertSingleSandboxResources(t, harness, originalID, 1, 1)
}
