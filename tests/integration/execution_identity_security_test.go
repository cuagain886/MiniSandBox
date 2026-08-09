//go:build integration

package integration

import (
	"archive/tar"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestExecutionIdentityHasNoRootOrCapabilities 验证真实容器中的用户命令身份及同 UID 自我 DoS 边界。
func TestExecutionIdentityHasNoRootOrCapabilities(t *testing.T) {
	const executionIdentity = "65532:65532"
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)
	first := createSandbox(t, instance.baseURL, image)
	second := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(first.ID)
	harness.trackSandbox(second.ID)
	waitSandboxState(t, instance.baseURL, first.ID, protocol.SandboxStateRunning)
	waitSandboxState(t, instance.baseURL, second.ID, protocol.SandboxStateRunning)
	firstID := harness.runningContainerID(t, first.ID)
	secondID := harness.runningContainerID(t, second.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	inspection, err := harness.client.ContainerInspect(ctx, firstID, mobyclient.ContainerInspectOptions{})
	if err != nil || inspection.Container.Config == nil || inspection.Container.HostConfig == nil {
		t.Fatalf("inspect execution security container")
	}
	if inspection.Container.Config.User != "0:0" {
		t.Fatalf("bootstrap container user: %q", inspection.Container.Config.User)
	}
	assertCapabilitySet(t, "CapDrop", inspection.Container.HostConfig.CapDrop, []string{"ALL"})
	if !containsSecurityOption(inspection.Container.HostConfig.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("no-new-privileges missing: %v", inspection.Container.HostConfig.SecurityOpt)
	}

	if code := execAndWait(t, harness.client, firstID, executionIdentity, []string{"/bin/sh", "-c", "cat /proc/self/status > /tmp/execution-status"}); code != 0 {
		t.Fatalf("capture execution status: exit=%d", code)
	}
	status := copyRegularFile(t, harness.client, firstID, "/tmp/execution-status")
	assertDockerExecStatus(t, status, 65532, 65532)

	captureRunnerStatus := `for p in /proc/[0-9]*; do read c < "$p/comm" 2>/dev/null || continue; if [ "$c" = runnerd ]; then cat "$p/status" > /tmp/runner-status; exit $?; fi; done; exit 42`
	if code := execAndWait(t, harness.client, firstID, executionIdentity, []string{"/bin/sh", "-c", captureRunnerStatus}); code != 0 {
		t.Fatalf("capture runner restricted status: exit=%d", code)
	}
	assertRestrictedStatus(t, copyRegularFile(t, harness.client, firstID, "/tmp/runner-status"), 65532, 65532)

	readRunnerEnv := `for p in /proc/[0-9]*; do read c < "$p/comm" 2>/dev/null || continue; if [ "$c" = runnerd ]; then cat "$p/environ" >/dev/null 2>&1; exit $?; fi; done; exit 42`
	if code := execAndWait(t, harness.client, firstID, executionIdentity, []string{"/bin/sh", "-c", readRunnerEnv}); code == 0 || code == 42 {
		t.Fatalf("same UID runner environ boundary: exit=%d", code)
	}

	killRunner := `for p in /proc/[0-9]*; do read c < "$p/comm" 2>/dev/null || continue; if [ "$c" = runnerd ]; then kill -TERM "${p#/proc/}"; exit $?; fi; done; exit 42`
	if code := execAndWait(t, harness.client, firstID, executionIdentity, []string{"/bin/sh", "-c", killRunner}); code != 0 {
		t.Fatalf("same UID self-DoS fixture: exit=%d", code)
	}
	firstClient := instance.runnerClient(t, first.ID)
	secondClient := instance.runnerClient(t, second.ID)
	if err := pollCondition(ctx, 25*time.Millisecond, func() (bool, error) {
		_, err := firstClient.Health(ctx, runnerbootstrap.CurrentProtocolVersion)
		return err != nil, nil
	}); err != nil {
		t.Fatalf("wait first runner readiness failure: %v", err)
	}
	if _, err := secondClient.Health(ctx, runnerbootstrap.CurrentProtocolVersion); err != nil {
		t.Fatalf("self-DoS crossed sandbox boundary: %v", err)
	}
	if secondInspection, err := harness.client.ContainerInspect(ctx, secondID, mobyclient.ContainerInspectOptions{}); err != nil || secondInspection.Container.State == nil || !secondInspection.Container.State.Running {
		t.Fatalf("second sandbox stopped after first self-DoS")
	}
}

func execAndWait(t *testing.T, client *mobyclient.Client, containerID, user string, command []string) int {
	t.Helper()
	created, err := client.ExecCreate(context.Background(), containerID, mobyclient.ExecCreateOptions{User: user, Cmd: command})
	if err != nil {
		t.Fatalf("create security exec")
	}
	if _, err := client.ExecStart(context.Background(), created.ID, mobyclient.ExecStartOptions{Detach: true}); err != nil {
		t.Fatalf("start security exec")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exitCode := -1
	if err := pollCondition(ctx, 10*time.Millisecond, func() (bool, error) {
		inspection, err := client.ExecInspect(ctx, created.ID, mobyclient.ExecInspectOptions{})
		if err != nil {
			return false, err
		}
		if inspection.Running {
			return false, nil
		}
		exitCode = inspection.ExitCode
		return true, nil
	}); err != nil {
		t.Fatalf("wait security exec: %v", err)
	}
	return exitCode
}

func copyRegularFile(t *testing.T, client *mobyclient.Client, containerID, path string) []byte {
	t.Helper()
	result, err := client.CopyFromContainer(context.Background(), containerID, mobyclient.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		t.Fatalf("copy security evidence")
	}
	defer result.Content.Close()
	reader := tar.NewReader(result.Content)
	header, err := reader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
		t.Fatalf("security evidence archive is invalid")
	}
	content, err := io.ReadAll(io.LimitReader(reader, header.Size))
	if err != nil {
		t.Fatalf("read security evidence")
	}
	return content
}

func assertRestrictedStatus(t *testing.T, content []byte, uid, gid uint32) {
	t.Helper()
	fields := parseStatusFields(content)
	assertIdentityAndCapabilities(t, fields, uid, gid)
	if groups := fields["Groups"]; len(groups) != 0 {
		t.Fatalf("supplementary groups are not empty: %v", groups)
	}
}

func assertDockerExecStatus(t *testing.T, content []byte, uid, gid uint32) {
	t.Helper()
	fields := parseStatusFields(content)
	assertIdentityAndCapabilities(t, fields, uid, gid)
	groups := fields["Groups"]
	// Docker Exec 会把 primary GID 同时放入 supplementary group；只允许这一项，
	// runner 自身仍由下一个断言证明 setgroups(nil) 后严格为空。
	if len(groups) > 1 || len(groups) == 1 && groups[0] != strconv.FormatUint(uint64(gid), 10) {
		t.Fatalf("docker exec has unexpected supplementary groups: %v", groups)
	}
}

func parseStatusFields(content []byte) map[string][]string {
	fields := make(map[string][]string)
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.Fields(line)
		if len(parts) > 0 {
			fields[strings.TrimSuffix(parts[0], ":")] = parts[1:]
		}
	}
	return fields
}

func assertIdentityAndCapabilities(t *testing.T, fields map[string][]string, uid, gid uint32) {
	t.Helper()
	if len(fields["Uid"]) != 4 || fields["Uid"][1] != strconv.FormatUint(uint64(uid), 10) || len(fields["Gid"]) != 4 || fields["Gid"][1] != strconv.FormatUint(uint64(gid), 10) {
		t.Fatalf("execution identity mismatch: Uid=%v Gid=%v", fields["Uid"], fields["Gid"])
	}
	if len(fields["CapEff"]) != 1 || fields["CapEff"][0] != "0000000000000000" {
		t.Fatalf("effective capabilities: %v", fields["CapEff"])
	}
}

func containsSecurityOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}
