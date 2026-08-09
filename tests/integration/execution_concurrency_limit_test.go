//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"minisandbox/pkg/protocol"
)

const integrationExecutionLimit = 2

// TestExecutionConcurrencyLimitIsAtomic 验证并发竞争不突破上限，完成和启动失败均释放 slot。
func TestExecutionConcurrencyLimitIsAtomic(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxdWithConfig(t, func(content string) string {
		return strings.Replace(content, "runner:\n  execution_uid: 65532\n  execution_gid: 65532", "runner:\n  execution_uid: 65532\n  execution_gid: 65532\n  max_concurrent_executions: 2", 1)
	})
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	registerBackgroundLogOwnershipCleanup(t, harness.client, containerID)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))

	start := make(chan struct{})
	results := make(chan backgroundPostResult, 6)
	var wait sync.WaitGroup
	for index := 0; index < 6; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- postPublicBackgroundResult(instance.baseURL, sandboxID, protocol.ExecuteRequest{
				Argv: []string{executionHelperPath, "process-tree", "kill"}, Background: true,
			})
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var accepted []protocol.ExecutionDescriptor
	limited := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent background request: %v", result.err)
		}
		switch result.status {
		case http.StatusAccepted:
			accepted = append(accepted, result.descriptor)
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("concurrent background status: %d body=%s", result.status, result.body)
		}
	}
	if len(accepted) != integrationExecutionLimit || limited != 4 {
		t.Fatalf("concurrency results: accepted=%d limited=%d", len(accepted), limited)
	}
	client := instance.runnerClient(t, sandboxID)
	for _, descriptor := range accepted {
		_ = waitProcessTreePIDs(t, client, descriptor.ExecutionID)
	}
	const countPath = "/workspace/p2-active-process-groups"
	if code := execAndWait(t, harness.client, containerID, "65532:65532", []string{executionHelperPath, "count-active-groups", countPath}); code != 0 {
		t.Fatalf("count active process groups: exit=%d", code)
	}
	count, err := strconv.Atoi(string(copyRegularFile(t, harness.client, containerID, countPath)))
	if err != nil || count > integrationExecutionLimit || count != len(accepted) {
		t.Fatalf("active process groups: got %d err=%v, accepted=%d", count, err, len(accepted))
	}
	for _, descriptor := range accepted {
		if _, err := client.Cancel(t.Context(), descriptor.ExecutionID); err != nil {
			t.Fatalf("release execution slot: %v", err)
		}
		_ = waitExecutionTerminal(t, client, descriptor.ExecutionID)
	}

	completed, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "exit", "0"}})
	if err != nil {
		t.Fatalf("reuse released slot: %v", err)
	}
	_ = waitExecutionTerminal(t, client, completed.ExecutionID)
	if _, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{"/missing/p2-start-failure"}}); err == nil {
		t.Fatal("missing executable unexpectedly started")
	}
	afterFailure, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{executionHelperPath, "exit", "0"}})
	if err != nil {
		t.Fatalf("start failure leaked slot: %v", err)
	}
	_ = waitExecutionTerminal(t, client, afterFailure.ExecutionID)
}

type backgroundPostResult struct {
	status     int
	descriptor protocol.ExecutionDescriptor
	body       string
	err        error
}

func postPublicBackgroundResult(baseURL, sandboxID string, request protocol.ExecuteRequest) backgroundPostResult {
	body, err := json.Marshal(request)
	if err != nil {
		return backgroundPostResult{err: err}
	}
	httpRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions", bytes.NewReader(body))
	if err != nil {
		return backgroundPostResult{err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return backgroundPostResult{err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return backgroundPostResult{err: err}
	}
	result := backgroundPostResult{status: response.StatusCode, body: string(raw)}
	if response.StatusCode == http.StatusAccepted {
		if err := json.Unmarshal(raw, &result.descriptor); err != nil {
			result.err = err
		}
	}
	return result
}
