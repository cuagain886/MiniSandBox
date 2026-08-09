//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestForegroundClientDisconnectTerminatesProcessGroup 验证公共 SSE 断开会取消活动前台进程组，而 terminal 后关闭保持 Exited。
func TestForegroundClientDisconnectTerminatesProcessGroup(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))

	t.Run("immediate_disconnect", func(t *testing.T) {
		response := postPublicForeground(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "process-tree", "kill"},
		})
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close immediate SSE body: %v", err)
		}
		waitNoProcessTreeCommands(t, harness, containerID)
	})

	t.Run("disconnect_after_output", func(t *testing.T) {
		response := postPublicForeground(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "process-tree", "kill"},
		})
		reader := bufio.NewReader(response.Body)
		var executionID string
		var output []byte
		for len(parseProcessTreePIDs(output)) != 3 {
			event := readPublicSSEEvent(t, reader)
			if executionID == "" {
				executionID = event.ExecutionID
			}
			if event.Type == protocol.EventStdout {
				chunk, err := base64.StdEncoding.DecodeString(event.DataBase64)
				if err != nil {
					t.Fatalf("decode PID output: %v", err)
				}
				output = append(output, chunk...)
			}
		}
		pids := parseProcessTreePIDs(output)
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close active SSE body: %v", err)
		}
		status := waitPublicExecutionTerminal(t, instance.baseURL, sandboxID, executionID)
		assertPublicTerminal(t, status, protocol.ExecutionStateCancelled, protocol.EventCancelled)
		waitContainerPIDsGone(t, harness, containerID, pids)
	})

	t.Run("close_after_terminal", func(t *testing.T) {
		response := postPublicForeground(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{
			Argv: []string{executionHelperPath, "exit", "0"},
		})
		reader := bufio.NewReader(response.Body)
		var terminal protocol.ExecutionEvent
		for !terminal.Terminal() {
			terminal = readPublicSSEEvent(t, reader)
		}
		if terminal.Type != protocol.EventExited {
			t.Fatalf("normal foreground terminal: %+v", terminal)
		}
		_ = response.Body.Close()
		status := waitPublicExecutionTerminal(t, instance.baseURL, sandboxID, terminal.ExecutionID)
		assertPublicTerminal(t, status, protocol.ExecutionStateExited, protocol.EventExited)
		time.Sleep(100 * time.Millisecond)
		status = getPublicExecutionStatus(t, instance.baseURL, sandboxID, terminal.ExecutionID)
		assertPublicTerminal(t, status, protocol.ExecutionStateExited, protocol.EventExited)
	})
}

func postPublicForeground(t *testing.T, baseURL, sandboxID string, request protocol.ExecuteRequest) *http.Response {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode public execution request: %v", err)
	}
	httpRequest, err := http.NewRequest(http.MethodPost, baseURL+"/v1/sandboxes/"+url.PathEscape(sandboxID)+"/executions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create public execution request: %v", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("post public foreground: %v", err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		defer response.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("public foreground response: status=%d body=%s", response.StatusCode, raw)
	}
	return response
}

func readPublicSSEEvent(t *testing.T, reader *bufio.Reader) protocol.ExecutionEvent {
	t.Helper()
	// runner 的空闲 keepalive 周期为 15 秒；测试等待窗口必须覆盖至少一次
	// keepalive，否则冷启动编译等合法静默命令会被测试工具误判为超时。
	deadline := time.Now().Add(lifecycleTimeout)
	var data []byte
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read public SSE: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(data) == 0 {
				continue
			}
			var event protocol.ExecutionEvent
			if err := json.Unmarshal(data, &event); err != nil || event.Validate() != nil {
				t.Fatalf("decode public SSE event: %v", err)
			}
			return event
		}
		if strings.HasPrefix(line, "data: ") {
			data = append(data, line[len("data: "):]...)
		}
	}
	t.Fatal("public SSE event timed out")
	return protocol.ExecutionEvent{}
}

func parseProcessTreePIDs(output []byte) []int {
	fields := strings.Fields(strings.TrimSpace(string(output)))
	if len(fields) < 3 {
		return nil
	}
	pids := make([]int, 0, 3)
	for _, field := range fields[:3] {
		_, value, ok := strings.Cut(field, "=")
		pid, err := strconv.Atoi(value)
		if !ok || err != nil || pid <= 1 {
			return nil
		}
		pids = append(pids, pid)
	}
	return pids
}

func waitNoProcessTreeCommands(t *testing.T, harness *dockerHarness, containerID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	stable := 0
	for time.Now().Before(deadline) {
		top, err := harness.client.ContainerTop(context.Background(), containerID, mobyclient.ContainerTopOptions{Arguments: []string{"-eo", "pid,args"}})
		if err != nil {
			t.Fatalf("inspect process tree commands")
		}
		found := false
		for _, process := range top.Processes {
			if strings.Contains(strings.Join(process, " "), "minisandbox-execution-helper process-tree") {
				found = true
			}
		}
		if found {
			stable = 0
		} else {
			stable++
			if stable >= 3 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("foreground disconnect left process tree commands")
}

func waitPublicExecutionTerminal(t *testing.T, baseURL, sandboxID, executionID string) protocol.ExecutionStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status := getPublicExecutionStatus(t, baseURL, sandboxID, executionID)
		if status.TerminalEvent != nil {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("public execution did not reach terminal")
	return protocol.ExecutionStatus{}
}

func getPublicExecutionStatus(t *testing.T, baseURL, sandboxID, executionID string) protocol.ExecutionStatus {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/sandboxes/" + url.PathEscape(sandboxID) + "/executions/" + url.PathEscape(executionID))
	if err != nil {
		t.Fatalf("get public execution status: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public execution status code: %d", response.StatusCode)
	}
	var status protocol.ExecutionStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("decode public execution status: %v", err)
	}
	return status
}

func assertPublicTerminal(t *testing.T, status protocol.ExecutionStatus, state protocol.ExecutionState, eventType protocol.EventType) {
	t.Helper()
	if status.State != state || status.TerminalEvent == nil || status.TerminalEvent.Type != eventType {
		t.Fatalf("public execution terminal mismatch: %+v", status)
	}
}
