//go:build integration

// Package integration 提供只在显式 opt-in 时访问真实 Linux Docker 的验收测试。
//
// 每个测试使用随机 test label 和独立 data directory；cleanup 只能枚举当前
// test ID 的资源，不能按名称前缀批量删除其他测试或用户资源。
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
	dockerruntime "minisandbox/internal/runtime/docker"
)

const (
	integrationOptInEnv = "MINISANDBOX_INTEGRATION"
	dockerHostEnv       = "MINISANDBOX_TEST_DOCKER_HOST"
	testDataRootEnv     = "MINISANDBOX_TEST_DATA_ROOT"
	testIDLabel         = "io.minisandbox.integration-test-id"
	cleanupTimeout      = 30 * time.Second
)

// dockerHarness 保存单个 integration test 的隔离资源。
type dockerHarness struct {
	t             *testing.T
	client        *mobyclient.Client
	testID        string
	dataDirectory string
	mu            sync.Mutex
	sandboxIDs    []string
	imageIDs      []string
}

// newDockerHarness 在显式 opt-in 后连接 Docker 并注册 finally cleanup。
func newDockerHarness(t *testing.T) *dockerHarness {
	t.Helper()
	if os.Getenv(integrationOptInEnv) != "1" {
		t.Skip("set MINISANDBOX_INTEGRATION=1 to run Docker integration tests")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Docker integration suite requires a linux/amd64 host")
	}

	testID := randomTestID(t)
	host := os.Getenv(dockerHostEnv)
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	client, err := mobyclient.New(
		mobyclient.WithHost(host),
		mobyclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Fatalf("create Docker integration client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := client.Ping(
		ctx,
		mobyclient.PingOptions{NegotiateAPIVersion: true},
	); err != nil {
		_ = client.Close()
		t.Fatalf("ping Docker integration daemon")
	}

	harness := &dockerHarness{
		t:             t,
		client:        client,
		testID:        testID,
		dataDirectory: integrationDataDirectory(t, testID),
	}
	if err := os.MkdirAll(harness.dataDirectory, 0o700); err != nil {
		_ = client.Close()
		t.Fatalf("create integration data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := harness.cleanup(); err != nil {
			t.Errorf("integration cleanup failed: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("close integration Docker client")
		}
	})
	return harness
}

// integrationDataDirectory 返回当前测试独占且不会超出 Unix Socket 上限的数据目录。
//
// 默认继续使用 t.TempDir；Docker Desktop WSL 验收可显式提供 daemon 与 WSL
// 都能访问的短根目录。自定义根目录必须是绝对路径，测试结束时只删除由随机
// test ID 派生的直接子目录。
func integrationDataDirectory(t *testing.T, testID string) string {
	t.Helper()
	root := os.Getenv(testDataRootEnv)
	if root == "" {
		return filepath.Join(t.TempDir(), "data")
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("integration data root must be absolute")
	}
	root = filepath.Clean(root)
	directory := filepath.Join(root, testID[:8])
	relative, err := filepath.Rel(root, directory)
	if err != nil ||
		relative != testID[:8] ||
		filepath.IsAbs(relative) ||
		strings.Contains(relative, string(filepath.Separator)) {
		t.Fatalf("integration data directory escaped configured root")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create custom integration data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove custom integration data directory: %v", err)
		}
	})
	return directory
}

// labels 返回当前测试唯一的资源 label。
func (h *dockerHarness) labels() map[string]string {
	return map[string]string{testIDLabel: h.testID}
}

// trackSandbox 登记由当前测试 API 创建的 ID，供 finally cleanup 精确过滤。
func (h *dockerHarness) trackSandbox(sandboxID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sandboxIDs = append(h.sandboxIDs, sandboxID)
}

// trackImage 登记当前测试创建的诊断镜像 ID，供 finally cleanup 精确删除。
func (h *dockerHarness) trackImage(imageID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.imageIDs = append(h.imageIDs, imageID)
}

// cleanup 按当前 test label 先删 container、再删 volume，最后删诊断镜像。
//
// 错误只报告失败阶段，不包含 Docker host、原始响应或 labels。
func (h *dockerHarness) cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	failures := make([]string, 0, 3)
	h.mu.Lock()
	sandboxIDs := append([]string(nil), h.sandboxIDs...)
	imageIDs := append([]string(nil), h.imageIDs...)
	h.mu.Unlock()
	filters := []string{testIDLabel + "=" + h.testID}
	for _, sandboxID := range sandboxIDs {
		filters = append(filters, dockerruntime.LabelSandboxID+"="+sandboxID)
	}

	for _, label := range filters {
		containers, err := h.client.ContainerList(
			ctx,
			mobyclient.ContainerListOptions{
				All:     true,
				Filters: make(mobyclient.Filters).Add("label", label),
			},
		)
		if err != nil {
			failures = append(failures, "list containers")
		} else {
			for _, container := range containers.Items {
				_, removeErr := h.client.ContainerRemove(
					ctx,
					container.ID,
					mobyclient.ContainerRemoveOptions{
						Force:         true,
						RemoveVolumes: false,
					},
				)
				if removeErr != nil && !cerrdefs.IsNotFound(removeErr) {
					failures = append(failures, "remove container")
				}
			}
		}

		volumes, err := h.client.VolumeList(
			ctx,
			mobyclient.VolumeListOptions{
				Filters: make(mobyclient.Filters).Add("label", label),
			},
		)
		if err != nil {
			failures = append(failures, "list volumes")
		} else {
			for _, volume := range volumes.Items {
				_, removeErr := h.client.VolumeRemove(
					ctx,
					volume.Name,
					mobyclient.VolumeRemoveOptions{Force: true},
				)
				if removeErr != nil && !cerrdefs.IsNotFound(removeErr) {
					failures = append(failures, "remove volume")
				}
			}
		}
	}
	for _, imageID := range imageIDs {
		_, err := h.client.ImageRemove(
			ctx,
			imageID,
			mobyclient.ImageRemoveOptions{
				Force:         true,
				PruneChildren: false,
			},
		)
		if err != nil && !cerrdefs.IsNotFound(err) {
			failures = append(failures, "remove image")
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, ", "))
	}
	return nil
}

// resourceCounts 返回当前 test label 仍关联的 container 和 volume 数量。
func (h *dockerHarness) resourceCounts(
	ctx context.Context,
) (int, int, error) {
	containers, err := h.client.ContainerList(
		ctx,
		mobyclient.ContainerListOptions{
			All: true,
			Filters: make(mobyclient.Filters).Add(
				"label",
				testIDLabel+"="+h.testID,
			),
		},
	)
	if err != nil {
		return 0, 0, errors.New("list labeled containers")
	}
	volumes, err := h.client.VolumeList(
		ctx,
		mobyclient.VolumeListOptions{
			Filters: make(mobyclient.Filters).Add(
				"label",
				testIDLabel+"="+h.testID,
			),
		},
	)
	if err != nil {
		return 0, 0, errors.New("list labeled volumes")
	}
	return len(containers.Items), len(volumes.Items), nil
}

// randomTestID 生成不包含测试名称或路径的随机 label value。
func randomTestID(t *testing.T) string {
	t.Helper()
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatalf("generate integration test ID")
	}
	return hex.EncodeToString(value[:])
}

// TestDockerHarnessEmptyCleanup 验证空清理可重复执行且不产生资源。
func TestDockerHarnessEmptyCleanup(t *testing.T) {
	harness := newDockerHarness(t)
	if err := harness.cleanup(); err != nil {
		t.Fatalf("first empty cleanup: %v", err)
	}
	if err := harness.cleanup(); err != nil {
		t.Fatalf("second empty cleanup: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	containers, volumes, err := harness.resourceCounts(ctx)
	if err != nil {
		t.Fatalf("count resources: %v", err)
	}
	if containers != 0 || volumes != 0 {
		t.Fatalf(
			"empty cleanup left resources: containers=%d volumes=%d",
			containers,
			volumes,
		)
	}
}

// TestDockerHarnessCleanupIsScopedToCurrentLabel 验证 cleanup 不删除其他 test ID。
func TestDockerHarnessCleanupIsScopedToCurrentLabel(t *testing.T) {
	harness := newDockerHarness(t)
	foreignID := randomTestID(t)
	ownName := "minisandbox-integration-" + harness.testID
	foreignName := "minisandbox-integration-" + foreignID
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	if _, err := harness.client.VolumeCreate(
		ctx,
		mobyclient.VolumeCreateOptions{
			Name:   ownName,
			Labels: harness.labels(),
		},
	); err != nil {
		t.Fatalf("create owned test volume")
	}
	if _, err := harness.client.VolumeCreate(
		ctx,
		mobyclient.VolumeCreateOptions{
			Name: foreignName,
			Labels: map[string]string{
				testIDLabel: foreignID,
			},
		},
	); err != nil {
		t.Fatalf("create foreign test volume")
	}
	defer func() {
		_, _ = harness.client.VolumeRemove(
			context.Background(),
			foreignName,
			mobyclient.VolumeRemoveOptions{Force: true},
		)
	}()

	if err := harness.cleanup(); err != nil {
		t.Fatalf("cleanup owned resources: %v", err)
	}
	if _, err := harness.client.VolumeInspect(
		ctx,
		ownName,
		mobyclient.VolumeInspectOptions{},
	); !cerrdefs.IsNotFound(err) {
		t.Fatalf("owned volume still exists")
	}
	if _, err := harness.client.VolumeInspect(
		ctx,
		foreignName,
		mobyclient.VolumeInspectOptions{},
	); err != nil {
		t.Fatalf("cleanup crossed test label boundary")
	}
}
