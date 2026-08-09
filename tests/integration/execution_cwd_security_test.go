//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"minisandbox/internal/runnerclient"
	"minisandbox/pkg/protocol"
)

// TestExecutionCWDRejectsTraversalAndSymlinks 验证 cwd 只能是 workspace 内无 symlink 的真实目录。
func TestExecutionCWDRejectsTraversalAndSymlinks(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	sandboxID, containerID := startExecutionSandbox(t, harness, instance, image)
	client := instance.runnerClient(t, sandboxID)

	setup := [][]string{
		{"/bin/mkdir", "-p", "/workspace/real/nested"},
		{"/usr/bin/touch", "/workspace/file"},
		{"/bin/ln", "-s", "/workspace/real", "/workspace/link-inside"},
		{"/bin/ln", "-s", "/tmp", "/workspace/link-outside"},
		{"/bin/ln", "-s", "/workspace/real/nested", "/workspace/real/link-mid"},
	}
	for _, command := range setup {
		if code := execAndWait(t, harness.client, containerID, "65532:65532", command); code != 0 {
			t.Fatalf("prepare cwd fixture: command=%v exit=%d", command, code)
		}
	}
	if code := execAndWait(t, harness.client, containerID, "0:0", []string{"/bin/mkdir", "/workspace2"}); code != 0 {
		t.Fatalf("prepare similar-prefix fixture: exit=%d", code)
	}

	for _, test := range []struct {
		name string
		cwd  string
		want string
	}{
		{name: "default", want: "/workspace"},
		{name: "root", cwd: "/workspace", want: "/workspace"},
		{name: "real-child", cwd: "/workspace/real/nested", want: "/workspace/real/nested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := executeForeground(t, client, protocol.ExecuteRequest{Argv: []string{"/bin/pwd"}, Cwd: test.cwd})
			assertSuccessfulForegroundEvents(t, events)
			if got := strings.TrimSpace(string(collectStream(events, protocol.EventStdout))); got != test.want {
				t.Fatalf("pwd: got %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name string
		cwd  string
	}{
		{name: "dot-dot", cwd: "/workspace/../tmp"},
		{name: "file", cwd: "/workspace/file"},
		{name: "similar-prefix", cwd: "/workspace2"},
		{name: "final-inside-symlink", cwd: "/workspace/link-inside"},
		{name: "final-outside-symlink", cwd: "/workspace/link-outside"},
		{name: "intermediate-symlink", cwd: "/workspace/real/link-mid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.ExecuteBackground(t.Context(), protocol.ExecuteRequest{Argv: []string{"/bin/pwd"}, Cwd: test.cwd})
			var status *runnerclient.StatusError
			if !errors.As(err, &status) || status.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("invalid cwd status: %v", err)
			}
			raw := postPublicExecutionError(t, instance.baseURL, sandboxID, protocol.ExecuteRequest{Argv: []string{"/bin/pwd"}, Cwd: test.cwd})
			var envelope protocol.ErrorResponse
			if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Code != string(protocol.ErrorCodeInvalidCWD) {
				t.Fatalf("public invalid cwd response: code=%q err=%v", envelope.Error.Code, err)
			}
		})
	}
}
