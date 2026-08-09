//go:build integration

package integration

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/pkg/protocol"
)

// TestManagedResourceLabelsContainOnlyRecoveryMetadata 验证真实 Docker 资源的
// labels 精确遵守恢复协议，不携带配置路径、请求正文或 token 等秘密。
func TestManagedResourceLabelsContainOnlyRecoveryMetadata(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	containerID := harness.runningContainerID(t, sandbox.ID)
	container, err := harness.client.ContainerInspect(
		ctx,
		containerID,
		mobyclient.ContainerInspectOptions{},
	)
	if err != nil || container.Container.Config == nil {
		t.Fatal("inspect sandbox container labels")
	}
	workspaceName := "minisandbox-workspace-" + sandbox.ID
	volume, err := harness.client.VolumeInspect(
		ctx,
		workspaceName,
		mobyclient.VolumeInspectOptions{},
	)
	if err != nil {
		t.Fatal("inspect sandbox workspace labels")
	}

	assertReviewedManagedLabels(
		t,
		"container",
		container.Container.Config.Labels,
		sandbox.ID,
		workspaceName,
	)
	assertReviewedManagedLabels(
		t,
		"volume",
		volume.Volume.Labels,
		sandbox.ID,
		workspaceName,
	)
	if !maps.Equal(
		minisandboxLabels(container.Container.Config.Labels),
		minisandboxLabels(volume.Volume.Labels),
	) {
		t.Fatal("container and volume recovery labels differ")
	}

	// 这些值分别代表配置路径和完整请求正文中的敏感上下文。runner token
	// 由控制面内部生成且无法从测试读取，因此同时拒绝任何 label key/value
	// 出现 token 字样，防止以后新增字段时意外扩大恢复协议。
	forbidden := []string{
		harness.dataDirectory,
		"sandboxd.yaml",
		image,
		`{"image":"` + image + `"}`,
		"token",
	}
	assertLabelsExclude(t, "container", container.Container.Config.Labels, forbidden)
	assertLabelsExclude(t, "volume", volume.Volume.Labels, forbidden)
}

// TestReviewedManagedLabelsAllowPlatformLabels 验证 Docker Desktop 等平台附加的
// 非 MiniSandbox label 不会被误判为恢复协议漂移。
func TestReviewedManagedLabelsAllowPlatformLabels(t *testing.T) {
	const sandboxID = "971dd068-517e-4ba5-979b-ccb503478142"
	workspaceName := "minisandbox-workspace-" + sandboxID
	labels, err := dockerruntime.EncodeLabels(dockerruntime.ManagedLabels{
		SandboxID: sandboxID,
		SpecHash:  strings.Repeat("a", 64),
		Workspace: workspaceName,
	})
	if err != nil {
		t.Fatal("encode reviewed labels")
	}
	labels["desktop.docker.io/wsl-distro"] = "Ubuntu"

	assertReviewedManagedLabels(t, "container", labels, sandboxID, workspaceName)
}

// assertReviewedManagedLabels 验证 labels 的键集合和值语义均匹配 Phase 1 契约。
func assertReviewedManagedLabels(
	t *testing.T,
	resource string,
	labels map[string]string,
	sandboxID string,
	workspaceName string,
) {
	t.Helper()
	managed := minisandboxLabels(labels)
	wantKeys := []string{
		dockerruntime.LabelManaged,
		dockerruntime.LabelSandboxID,
		dockerruntime.LabelSchemaVersion,
		dockerruntime.LabelSpecHash,
		dockerruntime.LabelExpiresAt,
		dockerruntime.LabelWorkspace,
		dockerruntime.LabelRunnerProtocolVersion,
	}
	actualKeys := slices.Sorted(maps.Keys(managed))
	slices.Sort(wantKeys)
	if !slices.Equal(actualKeys, wantKeys) {
		t.Fatalf("%s labels keys: got %v, want %v", resource, actualKeys, wantKeys)
	}
	metadata, err := dockerruntime.ParseLabels(managed)
	if err != nil {
		t.Fatalf("%s labels do not satisfy recovery codec", resource)
	}
	if metadata.SandboxID != sandboxID || metadata.Workspace != workspaceName {
		t.Fatalf("%s labels identify a different sandbox", resource)
	}
}

// minisandboxLabels 只投影 MiniSandbox 拥有的 label 命名空间，避免把 Docker
// Desktop 或运维系统附加的元数据误当成恢复协议字段。
func minisandboxLabels(labels map[string]string) map[string]string {
	managed := make(map[string]string)
	for key, value := range labels {
		if strings.HasPrefix(key, "minisandbox.io/") {
			managed[key] = value
		}
	}
	return managed
}

// assertLabelsExclude 扫描 label 键和值，但错误不回显潜在秘密。
func assertLabelsExclude(
	t *testing.T,
	resource string,
	labels map[string]string,
	forbidden []string,
) {
	t.Helper()
	for key, value := range labels {
		candidate := strings.ToLower(key + "\x00" + value)
		for _, secret := range forbidden {
			if secret != "" && strings.Contains(
				candidate,
				strings.ToLower(secret),
			) {
				t.Fatalf("%s labels contain forbidden request or secret data", resource)
			}
		}
	}
}
