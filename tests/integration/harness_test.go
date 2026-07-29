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
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

const (
	integrationOptInEnv = "MINISANDBOX_INTEGRATION"
	dockerHostEnv       = "MINISANDBOX_TEST_DOCKER_HOST"
	testIDLabel         = "io.minisandbox.integration-test-id"
	cleanupTimeout      = 30 * time.Second
)

// dockerHarness 保存单个 integration test 的隔离资源。
type dockerHarness struct {
	t             *testing.T
	client        *mobyclient.Client
	testID        string
	dataDirectory string
}

// newDockerHarness 在显式 opt-in 后连接 Docker 并注册 finally cleanup。
func newDockerHarness(t *testing.T) *dockerHarness {
	t.Helper()
	if os.Getenv(integrationOptInEnv) != "1" {
		t.Skip("set MINISANDBOX_INTEGRATION=1 to run Docker integration tests")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Docker integration suite requires a Linux host")
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
		dataDirectory: filepath.Join(t.TempDir(), "data"),
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

// labels 返回当前测试唯一的资源 label。
func (h *dockerHarness) labels() map[string]string {
	return map[string]string{testIDLabel: h.testID}
}

// cleanup 按当前 test label 先删 container、再删 volume。
//
// 错误只报告失败阶段，不包含 Docker host、原始响应或 labels。
func (h *dockerHarness) cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	failures := make([]string, 0, 2)

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
			Filters: make(mobyclient.Filters).Add(
				"label",
				testIDLabel+"="+h.testID,
			),
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
