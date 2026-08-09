//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

const (
	masterKeySentinel  = "p2-master-key-sentinel-123456789"
	bootstrapSentinel  = "7654321"
	allowedEnvSentinel = "p2-allowed-env-sentinel"
)

// TestExecutionEnvironmentAndSecretsAreIsolated 验证用户进程、API、日志及 Docker inspect 均不泄露内部材料。
func TestExecutionEnvironmentAndSecretsAreIsolated(t *testing.T) {
	harness := newDockerHarness(t)
	keyPath := filepath.Join(harness.dataDirectory, "runner-master-key")
	if len(masterKeySentinel) != len(runnerauth.MasterKey{}) {
		t.Fatal("master key sentinel length is invalid")
	}
	if err := os.WriteFile(keyPath, []byte(masterKeySentinel), 0o400); err != nil {
		t.Fatalf("write deterministic master key: %v", err)
	}

	var serviceLogs synchronizedBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&serviceLogs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		return strings.Replace(content, "runner:\n  execution_uid: 65532\n  execution_gid: 65532", "runner:\n  execution_uid: 65532\n  execution_gid: 65532\n  max_output_bytes: "+bootstrapSentinel, 1)
	})
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))

	token, err := runnerauth.DeriveToken(&instance.key, sandboxID)
	if err != nil {
		t.Fatalf("derive test runner token: %v", err)
	}
	tokenSentinel := base64.RawURLEncoding.EncodeToString(token[:])
	token.Clear()
	for _, name := range []string{runnerbootstrap.ConfigFileName, runnerauth.CredentialFileName} {
		if _, err := os.Lstat(filepath.Join(instance.runRoot, sandboxID, name)); !os.IsNotExist(err) {
			t.Fatalf("one-time bootstrap material remains: name=%s err=%v", name, err)
		}
	}

	descriptor := postPublicBackground(t, t.Context(), instance.baseURL, sandboxID, protocol.ExecuteRequest{
		Argv: []string{executionHelperPath, "environment"}, Background: true,
		Env: map[string]string{
			"P2_ALLOWED":              allowedEnvSentinel,
			"MINISANDBOX_MASTER_KEY":  masterKeySentinel,
			"RUNNER_TOKEN":            tokenSentinel,
			"RUNNER_BOOTSTRAP_CONFIG": bootstrapSentinel,
			"LD_PRELOAD":              masterKeySentinel,
		},
	})
	status := waitPublicExecutionTerminal(t, instance.baseURL, sandboxID, descriptor.ExecutionID)
	page := waitPublicExecutionLogs(t, instance.baseURL, sandboxID, descriptor.ExecutionID)
	environment := string(collectStream(page.Events, protocol.EventStdout))
	if !strings.Contains(environment, "P2_ALLOWED="+allowedEnvSentinel) {
		t.Fatal("allowed request environment is not visible")
	}
	for _, key := range []string{"MINISANDBOX_MASTER_KEY=", "RUNNER_TOKEN=", "RUNNER_BOOTSTRAP_CONFIG=", "LD_PRELOAD="} {
		if strings.Contains(environment, key) {
			t.Fatal("internal environment key reached the user process")
		}
	}

	invalidEnvResponse := postPublicExecutionError(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{
		Argv: []string{executionHelperPath, "environment"}, Env: map[string]string{"BAD=KEY": tokenSentinel},
	})
	startErrorResponse := postPublicExecutionError(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{
		Argv: []string{"/missing/" + masterKeySentinel},
	})
	var invalidEnvelope protocol.ErrorResponse
	if err := json.Unmarshal(invalidEnvResponse, &invalidEnvelope); err != nil || invalidEnvelope.Error.Code != string(protocol.ErrorCodeInvalidExecutionRequest) {
		t.Fatalf("invalid environment error is not stable: code=%q err=%v", invalidEnvelope.Error.Code, err)
	}

	container, err := harness.client.ContainerInspect(context.Background(), containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect sandbox container: %v", err)
	}
	inspectJSON, err := json.Marshal(container.Container)
	if err != nil {
		t.Fatalf("encode Docker inspect: %v", err)
	}
	apiJSON, err := json.Marshal([]any{descriptor, status, page})
	if err != nil {
		t.Fatalf("encode API evidence: %v", err)
	}
	for name, evidence := range map[string][]byte{
		"user environment": []byte(environment),
		"public API":       append(append(apiJSON, invalidEnvResponse...), startErrorResponse...),
		"service logs":     serviceLogs.Bytes(),
		"Docker inspect":   inspectJSON,
	} {
		assertEvidenceExcludesSentinels(t, name, evidence, masterKeySentinel, tokenSentinel, bootstrapSentinel)
	}
}

func postPublicExecutionError(t *testing.T, baseURL, sandboxID string, request protocol.ExecuteRequest) []byte {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode invalid execution request: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create invalid execution request: %v", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("post invalid execution request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatalf("read invalid execution response: %v", err)
	}
	if response.StatusCode < 400 || response.StatusCode >= 500 {
		t.Fatalf("invalid execution status: %d", response.StatusCode)
	}
	return raw
}

func assertEvidenceExcludesSentinels(t *testing.T, name string, evidence []byte, sentinels ...string) {
	t.Helper()
	for _, sentinel := range sentinels {
		if bytes.Contains(evidence, []byte(sentinel)) {
			t.Fatalf("%s contains an internal sentinel", name)
		}
	}
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(content)
}

func (b *synchronizedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}
