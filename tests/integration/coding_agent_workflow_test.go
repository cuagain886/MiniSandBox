//go:build integration

package integration

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

const integrationAgentImageEnv = "MINISANDBOX_TEST_AGENT_IMAGE"

// TestCodingAgentLocalGitWorkflow 验证 coding-agent 镜像只能经 sidecar 访问本地 Git
// 夹具，并通过公共 execution API 完成 clone、argv test、shell build 与可诊断失败。
func TestCodingAgentLocalGitWorkflow(t *testing.T) {
	agentImage := os.Getenv(integrationAgentImageEnv)
	egressImage := os.Getenv(integrationEgressImageEnv)
	if !strings.Contains(agentImage, "@sha256:") || !strings.Contains(egressImage, "@sha256:") {
		t.Skip("set digest-pinned MINISANDBOX_TEST_AGENT_IMAGE and MINISANDBOX_TEST_EGRESS_IMAGE")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("host git is required to prepare the local dumb-HTTP fixture")
	}
	harness := newDockerHarness(t)
	info, err := harness.client.Info(t.Context(), mobyclient.InfoOptions{})
	if err == nil && strings.Contains(strings.ToLower(info.Info.OperatingSystem), "docker desktop") {
		t.Skip("native Linux Docker is required for host netns inode attestation")
	}
	const publicAddress = "11.254.252.1"
	addHostAddress(t, publicAddress)
	repositoryRoot := prepareGitFixture(t)
	listener := listenFixture(t, publicAddress)
	server := &http.Server{Handler: http.FileServer(http.Dir(repositoryRoot)), ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	go func() { _ = server.Serve(listener) }()
	repositoryURL := "http://" + listener.Addr().String() + "/agent.git"

	harness.ensureImage(t, agentImage)
	harness.ensureImage(t, egressImage)
	instance := harness.startSandboxdWithConfig(t, outboundConfig(egressImage, ""))
	sandbox := createSandboxWithNetwork(t, instance.baseURL, agentImage, true)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateRunning)

	assertPublicExecutionExit(t, instance.baseURL, sandbox.ID, protocol.ExecuteRequest{
		Argv: []string{"git", "clone", repositoryURL, "/workspace/agent-repo"},
		Env:  offlineGoEnvironment(),
	}, 0)
	assertPublicExecutionExit(t, instance.baseURL, sandbox.ID, protocol.ExecuteRequest{
		Argv: []string{"go", "test", "./..."}, Cwd: "/workspace/agent-repo", Env: offlineGoEnvironment(),
	}, 0)
	assertPublicExecutionExit(t, instance.baseURL, sandbox.ID, protocol.ExecuteRequest{
		Shell: "go build -o /workspace/agent-app . && test -x /workspace/agent-app",
		Cwd:   "/workspace/agent-repo", Env: offlineGoEnvironment(),
	}, 0)

	failingEnvironment := offlineGoEnvironment()
	failingEnvironment["EXPECT_FAILURE"] = "1"
	assertPublicExecutionExit(t, instance.baseURL, sandbox.ID, protocol.ExecuteRequest{
		Argv: []string{"go", "test", "./..."}, Cwd: "/workspace/agent-repo", Env: failingEnvironment,
	}, 1)
	unreachableURL := "http://" + listener.Addr().String() + "/missing.git"
	assertPublicExecutionNonZero(t, instance.baseURL, sandbox.ID, protocol.ExecuteRequest{
		Argv: []string{"git", "clone", unreachableURL, "/workspace/unreachable"}, Env: offlineGoEnvironment(), TimeoutSeconds: 5,
	})

	mainID := harness.runningContainerID(t, sandbox.ID)
	sidecarName := "minisandbox-egress-" + sandbox.ID
	if submitSandboxDelete(t, instance.baseURL, sandbox.ID) != http.StatusAccepted {
		t.Fatal("delete coding-agent sandbox")
	}
	waitSandboxState(t, instance.baseURL, sandbox.ID, protocol.SandboxStateTerminated)
	for _, name := range []string{mainID, sidecarName} {
		if _, err := harness.client.ContainerInspect(t.Context(), name, mobyclient.ContainerInspectOptions{}); err == nil || !cerrdefs.IsNotFound(err) {
			t.Fatalf("workflow cleanup retained container %s: %v", name, err)
		}
	}
}

func prepareGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	bare := filepath.Join(root, "agent.git")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("create Git fixture source: %v", err)
	}
	files := map[string]string{
		"go.mod":       "module fixture.local/agent\n\ngo 1.22\n",
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Message()) }\n\nfunc Message() string { return \"agent-ready\" }\n",
		"main_test.go": "package main\n\nimport (\"os\"; \"testing\")\n\nfunc TestWorkflow(t *testing.T) { if Message() != \"agent-ready\" { t.Fatal(\"unexpected message\") }; if os.Getenv(\"EXPECT_FAILURE\") == \"1\" { t.Fatal(\"expected workflow failure\") } }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write Git fixture: %v", err)
		}
	}
	runGitFixtureCommand(t, source, "init", "--initial-branch=main")
	runGitFixtureCommand(t, source, "config", "user.name", "MiniSandbox Integration")
	runGitFixtureCommand(t, source, "config", "user.email", "integration@invalid.example")
	runGitFixtureCommand(t, source, "add", "go.mod", "main.go", "main_test.go")
	runGitFixtureCommand(t, source, "commit", "-m", "fixture")
	command := exec.Command("git", "clone", "--bare", source, bare)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create bare Git fixture: %v: %s", err, output)
	}
	runGitFixtureCommand(t, bare, "update-server-info")
	return root
}

func runGitFixtureCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare Git fixture: %v: %s", err, output)
	}
}

func offlineGoEnvironment() map[string]string {
	return map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "NO_PROXY": "*",
		"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "http_proxy": "", "https_proxy": "", "all_proxy": "",
	}
}

func assertPublicExecutionExit(t *testing.T, baseURL, sandboxID string, request protocol.ExecuteRequest, want int) {
	t.Helper()
	terminal := publicExecutionTerminal(t, baseURL, sandboxID, request)
	if terminal.Type != protocol.EventExited || terminal.ExitCode == nil || *terminal.ExitCode != want {
		t.Fatalf("public execution terminal: got %+v, want exited(%d)", terminal, want)
	}
}

func assertPublicExecutionNonZero(t *testing.T, baseURL, sandboxID string, request protocol.ExecuteRequest) {
	t.Helper()
	terminal := publicExecutionTerminal(t, baseURL, sandboxID, request)
	if terminal.Type != protocol.EventExited || terminal.ExitCode == nil || *terminal.ExitCode == 0 {
		t.Fatalf("dependency failure terminal is not a non-zero exit: %+v", terminal)
	}
}

func publicExecutionTerminal(t *testing.T, baseURL, sandboxID string, request protocol.ExecuteRequest) protocol.ExecutionEvent {
	t.Helper()
	response := postPublicForeground(t, baseURL, sandboxID, request)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for {
		event := readPublicSSEEvent(t, reader)
		if event.Terminal() {
			return event
		}
	}
}
