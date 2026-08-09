//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minisandbox/internal/runnerauth"
	"minisandbox/pkg/protocol"
)

const shellIdentitySource = `if [ -n "${BASH_VERSION:-}" ]; then printf bash; else printf sh; fi`

// TestExecutionShellFallbackAndMissingError 验证 bash 优先、sh fallback，以及缺失或不可执行时的稳定安全错误。
func TestExecutionShellFallbackAndMissingError(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	t.Run("bash_preferred", func(t *testing.T) {
		sandboxID, _ := startExecutionSandbox(t, harness, instance, image)
		assertShellIdentity(t, instance, sandboxID, "bash")
	})
	t.Run("sh_fallback", func(t *testing.T) {
		sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
		if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/mv", "/bin/bash", "/bin/bash.p2-disabled"}); code != 0 {
			t.Fatalf("disable bash: exit=%d", code)
		}
		assertShellIdentity(t, instance, sandboxID, "sh")
	})
	t.Run("shells_missing", func(t *testing.T) {
		sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
		for _, shell := range []string{"bash", "sh"} {
			if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/mv", "/bin/" + shell, "/bin/" + shell + ".p2-disabled"}); code != 0 {
				t.Fatalf("disable %s: exit=%d", shell, code)
			}
		}
		assertShellNotFound(t, instance, sandboxID)
	})
	t.Run("shells_not_executable", func(t *testing.T) {
		sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
		if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/chmod", "0644", "/bin/bash", "/bin/sh"}); code != 0 {
			t.Fatalf("remove shell execute bits: exit=%d", code)
		}
		assertShellNotFound(t, instance, sandboxID)
	})
}

func startExecutionSandbox(t *testing.T, harness *dockerHarness, instance *sandboxdInstance, image string) (string, string) {
	t.Helper()
	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)
	return sandbox.ID, harness.runningContainerID(t, sandbox.ID)
}

func assertShellIdentity(t *testing.T, instance *sandboxdInstance, sandboxID, want string) {
	t.Helper()
	events := executeForeground(t, instance.runnerClient(t, sandboxID), protocol.ExecuteRequest{Shell: shellIdentitySource})
	assertSuccessfulForegroundEvents(t, events)
	if got := string(collectStream(events, protocol.EventStdout)); got != want {
		t.Fatalf("shell identity: got %q, want %q", got, want)
	}
}

func assertShellNotFound(t *testing.T, instance *sandboxdInstance, sandboxID string) {
	t.Helper()
	const secretSource = "printf PHASE2_SHELL_SOURCE_MUST_NOT_LEAK"
	status, response, raw := postRunnerExecution(t, instance, sandboxID, protocol.ExecuteRequest{Shell: secretSource})
	if status != http.StatusUnprocessableEntity || response.Error.Code != string(protocol.ErrorCodeShellNotFound) {
		t.Fatalf("shell missing response: status=%d body=%+v", status, response)
	}
	if strings.Contains(string(raw), secretSource) || strings.Contains(string(raw), "started") {
		t.Fatalf("shell error leaked source or fabricated started event: %s", raw)
	}
}

func postRunnerExecution(t *testing.T, instance *sandboxdInstance, sandboxID string, request protocol.ExecuteRequest) (int, protocol.ErrorResponse, []byte) {
	t.Helper()
	token, err := runnerauth.DeriveToken(&instance.key, sandboxID)
	if err != nil {
		t.Fatalf("derive runner token: %v", err)
	}
	defer token.Clear()
	encodedToken := base64.RawURLEncoding.EncodeToString(token[:])
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode runner request: %v", err)
	}
	socketPath := filepath.Join(instance.runRoot, sandboxID, "runner.sock")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runner/v1/executions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create runner request: %v", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+encodedToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpResponse, err := (&http.Client{Transport: transport}).Do(httpRequest)
	if err != nil {
		t.Fatalf("post runner execution: %v", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, 64*1024))
	if err != nil {
		t.Fatalf("read runner response: %v", err)
	}
	var decoded protocol.ErrorResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode runner error: %v", err)
	}
	return httpResponse.StatusCode, decoded, raw
}
