//go:build integration

package integration

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestExecutionStartFailuresAreSafeAndLeaveNoProcess 验证不存在、不可执行文件和目录都在 headers 前稳定失败，且不产生子进程。
func TestExecutionStartFailuresAreSafeAndLeaveNoProcess(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	installExecutionHelper(t, harness.client, containerID, buildExecutionHelper(t))
	const nonExecutable = "/tmp/p2-start-secret-noexec"
	if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/cp", executionHelperPath, nonExecutable}); code != 0 {
		t.Fatalf("copy non-executable fixture: exit=%d", code)
	}
	if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/chmod", "0644", nonExecutable}); code != 0 {
		t.Fatalf("chmod non-executable fixture: exit=%d", code)
	}
	baseline := containerProcessTable(t, harness, containerID)

	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "not_found", argv: []string{"/tmp/p2-start-secret-missing", "sensitive-argument"}},
		{name: "permission_denied", argv: []string{nonExecutable, "sensitive-argument"}},
		{name: "directory", argv: []string{"/workspace", "sensitive-argument"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, response, raw := postRunnerExecution(t, instance, sandboxID, protocol.ExecuteRequest{Argv: test.argv})
			if status != http.StatusUnprocessableEntity || response.Error.Code != "EXECUTION_START_FAILED" {
				t.Fatalf("start failure response: status=%d body=%+v", status, response)
			}
			body := string(raw)
			for _, forbidden := range []string{test.argv[0], test.argv[1], harness.dataDirectory, `"type":"started"`, `"type":"exited"`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("start failure leaked forbidden content %q: %s", forbidden, body)
				}
			}
			if after := containerProcessTable(t, harness, containerID); !reflect.DeepEqual(after, baseline) {
				t.Fatalf("start failure changed process table: before=%v after=%v", baseline, after)
			}
		})
	}
	assertRunnerHealthy(t, instance, sandboxID)
}

func containerProcessTable(t *testing.T, harness *dockerHarness, containerID string) [][]string {
	t.Helper()
	top, err := harness.client.ContainerTop(context.Background(), containerID, mobyclient.ContainerTopOptions{Arguments: []string{"-eo", "pid,ppid,args"}})
	if err != nil {
		t.Fatalf("inspect container process table")
	}
	result := make([][]string, len(top.Processes))
	for index, process := range top.Processes {
		result[index] = append([]string(nil), process...)
	}
	return result
}
