//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	mobymount "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"
	"minisandbox/pkg/protocol"
)

// TestSandboxContainerUsesFixedPhase1SecurityProfile 验证真实 container 安全边界。
func TestSandboxContainerUsesFixedPhase1SecurityProfile(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)
	containerID := harness.runningContainerID(t, sandbox.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspection, err := harness.client.ContainerInspect(
		ctx,
		containerID,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil {
		t.Fatalf("inspect sandbox security profile")
	}
	container := inspection.Container
	if container.Config == nil ||
		container.HostConfig == nil ||
		container.NetworkSettings == nil {
		t.Fatal("Docker inspect omitted security configuration")
	}
	host := container.HostConfig
	config := container.Config

	if host.Privileged {
		t.Fatal("sandbox container is privileged")
	}
	if string(host.NetworkMode) != "none" || !config.NetworkDisabled {
		t.Fatalf(
			"network boundary: mode=%q disabled=%v",
			host.NetworkMode,
			config.NetworkDisabled,
		)
	}
	assertCapabilitySet(t, "CapDrop", host.CapDrop, []string{"ALL"})
	assertCapabilitySet(
		t,
		"CapAdd",
		host.CapAdd,
		[]string{"CHOWN", "SETUID", "SETGID", "KILL"},
	)
	if !slices.Contains(host.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("NoNewPrivileges missing: %v", host.SecurityOpt)
	}
	for _, option := range host.SecurityOpt {
		if strings.Contains(option, "seccomp=unconfined") {
			t.Fatalf("default seccomp disabled: %v", host.SecurityOpt)
		}
	}
	if host.ReadonlyRootfs {
		t.Fatal("Phase 1 unexpectedly enabled read-only rootfs")
	}
	if config.User != "0:0" {
		t.Fatalf("container user: got %q, want 0:0", config.User)
	}
	if host.PublishAllPorts ||
		len(host.PortBindings) != 0 ||
		len(config.ExposedPorts) != 0 ||
		len(container.NetworkSettings.Ports) != 0 {
		t.Fatal("sandbox container published or exposed ports")
	}
	if len(host.Binds) != 0 {
		t.Fatalf("unreviewed legacy bind mounts: %v", host.Binds)
	}

	const (
		wantNanoCPUs = int64(500_000_000)
		wantMemory   = int64(256 * 1024 * 1024)
		wantPIDs     = int64(64)
	)
	if host.NanoCPUs != wantNanoCPUs ||
		host.Memory != wantMemory ||
		host.PidsLimit == nil ||
		*host.PidsLimit != wantPIDs {
		t.Fatalf(
			"resource limits: nano_cpus=%d memory=%d pids=%v",
			host.NanoCPUs,
			host.Memory,
			host.PidsLimit,
		)
	}

	if len(container.Mounts) != 2 {
		t.Fatalf("mount count: got %d, want 2", len(container.Mounts))
	}
	wantRuntimeSource := filepath.Join(
		harness.dataDirectory,
		"run",
		sandbox.ID,
	)
	wantWorkspace := "minisandbox-workspace-" + sandbox.ID
	var runtimeMount, workspaceMount bool
	for _, mount := range container.Mounts {
		if strings.Contains(mount.Source, "docker.sock") ||
			strings.Contains(mount.Destination, "docker.sock") {
			t.Fatal("sandbox container received Docker socket")
		}
		switch {
		case mount.Type == mobymount.TypeBind &&
			sameRuntimeBindSource(mount.Source, wantRuntimeSource) &&
			mount.Destination == "/run/minisandbox" &&
			mount.RW:
			runtimeMount = true
		case mount.Type == mobymount.TypeVolume &&
			mount.Name == wantWorkspace &&
			mount.Destination == "/workspace" &&
			mount.RW:
			workspaceMount = true
		default:
			t.Fatalf("unexpected sandbox mount: %#v", mount)
		}
	}
	if !runtimeMount || !workspaceMount {
		t.Fatalf(
			"required mounts: runtime=%v workspace=%v",
			runtimeMount,
			workspaceMount,
		)
	}
}

// sameRuntimeBindSource 验证 Docker inspect 返回的 bind source 确实指向预期目录。
//
// Docker Desktop 的 WSL 代理会把创建请求中的 Linux 路径改写到 dockerd 的 mount
// namespace。普通 Linux 仍要求路径精确相等；发生该固定格式改写时，通过 WSL
// cross-distro 映射和 os.SameFile 比较文件身份，避免仅凭目标路径接受任意 bind。
func sameRuntimeBindSource(actual, expected string) bool {
	if filepath.Clean(actual) == filepath.Clean(expected) {
		return true
	}

	const (
		dockerDesktopSourceRoot = "/run/desktop/mnt/host/wsl/docker-desktop-bind-mounts"
		wslSourceRoot           = "/mnt/wsl/docker-desktop-bind-mounts"
	)
	actual = filepath.Clean(actual)
	relative, err := filepath.Rel(dockerDesktopSourceRoot, actual)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	mappedSource := filepath.Join(wslSourceRoot, relative)
	mappedInfo, err := os.Stat(mappedSource)
	if err != nil {
		return false
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		return false
	}
	return os.SameFile(mappedInfo, expectedInfo)
}

// assertCapabilitySet 归一化 Docker inspect 的可选 `CAP_` 前缀后比较精确集合。
func assertCapabilitySet(
	t *testing.T,
	name string,
	actual []string,
	expected []string,
) {
	t.Helper()
	actual = append([]string(nil), actual...)
	expected = append([]string(nil), expected...)
	for index := range actual {
		actual[index] = strings.TrimPrefix(actual[index], "CAP_")
	}
	for index := range expected {
		expected[index] = strings.TrimPrefix(expected[index], "CAP_")
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s: got %v, want %v", name, actual, expected)
	}
}
