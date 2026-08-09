//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	controlapi "minisandbox/internal/api"
	"minisandbox/internal/bootstrap"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/pkg/protocol"
)

const (
	integrationImageEnv = "MINISANDBOX_TEST_IMAGE"
	lifecycleTimeout    = 2 * time.Minute
)

// sandboxdInstance 保存测试内运行的真实 bootstrap 和 HTTP 地址。
type sandboxdInstance struct {
	baseURL string
	cancel  context.CancelFunc
	done    chan error
}

// TestCreateSandboxEventuallyRunning 验证 POST、GET、Docker 与 runner health 完整链路。
func TestCreateSandboxEventuallyRunning(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	instance := harness.startSandboxd(t)

	sandbox := createSandbox(t, instance.baseURL, image)
	harness.trackSandbox(sandbox.ID)
	sandbox = waitSandboxState(
		t,
		instance.baseURL,
		sandbox.ID,
		protocol.SandboxStateRunning,
	)

	containerID := harness.runningContainerID(t, sandbox.ID)
	if containerID == "" {
		t.Fatal("running sandbox has empty Docker container ID")
	}
	socketPath := filepath.Join(
		harness.dataDirectory,
		"run",
		sandbox.ID,
		"runner.sock",
	)
	client := runnerclient.New(socketPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Health(ctx, runnerbootstrap.CurrentProtocolVersion); err != nil {
		t.Fatalf("runner socket health failed: %v", err)
	}
}

// startSandboxd 使用当前 harness data directory 启动真实控制面。
func (h *dockerHarness) startSandboxd(t *testing.T) *sandboxdInstance {
	t.Helper()
	address := reserveLoopbackAddress(t)
	configPath := filepath.Join(h.dataDirectory, "sandboxd.yaml")
	dockerHost := os.Getenv(dockerHostEnv)
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	content := integrationConfig(h.dataDirectory, address, dockerHost)
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write sandboxd integration config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	instance := &sandboxdInstance{
		baseURL: "http://" + address,
		cancel:  cancel,
		done:    make(chan error, 1),
	}
	go func() {
		instance.done <- bootstrap.Run(ctx, bootstrap.Options{
			ConfigPath: configPath,
			Build: controlapi.BuildInfo{
				Version: "integration",
			},
		})
		close(instance.done)
	}()
	t.Cleanup(func() {
		instance.stop(t)
	})
	waitReady(t, instance)
	return instance
}

// stop 取消 sandboxd 并等待全部依赖逆序关闭。
func (s *sandboxdInstance) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			t.Errorf("sandboxd integration shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Errorf("sandboxd integration shutdown timed out")
	}
}

// waitReady 等待真实 `/readyz` 返回 200，启动失败则立即报告。
func waitReady(t *testing.T, instance *sandboxdInstance) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-instance.done:
			t.Fatalf("sandboxd exited before ready: %v", err)
		default:
		}
		response, err := http.Get(instance.baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("sandboxd did not become ready")
}

// createSandbox 提交真实创建请求并验证 202 与 Location。
func createSandbox(t *testing.T, baseURL, image string) protocol.Sandbox {
	t.Helper()
	body, err := json.Marshal(protocol.CreateSandboxRequest{Image: image})
	if err != nil {
		t.Fatalf("encode create request: %v", err)
	}
	response, err := http.Post(
		baseURL+"/v1/sandboxes",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("post sandbox: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status: got %d, want 202", response.StatusCode)
	}
	var sandbox protocol.Sandbox
	if err := json.NewDecoder(response.Body).Decode(&sandbox); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if got, want := response.Header.Get("Location"),
		"/v1/sandboxes/"+sandbox.ID; got != want {
		t.Fatalf("Location: got %q, want %q", got, want)
	}
	return sandbox
}

// waitSandboxState 轮询生命周期 API 直到目标状态或安全失败状态。
func waitSandboxState(
	t *testing.T,
	baseURL string,
	sandboxID string,
	target protocol.SandboxState,
) protocol.Sandbox {
	t.Helper()
	deadline := time.Now().Add(lifecycleTimeout)
	for time.Now().Before(deadline) {
		response, err := http.Get(
			baseURL + "/v1/sandboxes/" + sandboxID,
		)
		if err == nil {
			var sandbox protocol.Sandbox
			decodeErr := json.NewDecoder(response.Body).Decode(&sandbox)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil {
				if sandbox.State == target {
					return sandbox
				}
				if sandbox.State == protocol.SandboxStateFailed {
					t.Fatalf(
						"sandbox failed before %s: reason=%s",
						target,
						sandbox.Reason,
					)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("sandbox did not reach %s", target)
	return protocol.Sandbox{}
}

// runningContainerID 验证目标 sandbox 恰有一个 running container。
func (h *dockerHarness) runningContainerID(
	t *testing.T,
	sandboxID string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := h.client.ContainerList(
		ctx,
		mobyclient.ContainerListOptions{
			All: true,
			Filters: make(mobyclient.Filters).Add(
				"label",
				dockerruntime.LabelSandboxID+"="+sandboxID,
			),
		},
	)
	if err != nil {
		t.Fatalf("list sandbox containers")
	}
	if len(result.Items) != 1 {
		t.Fatalf("container count: got %d, want 1", len(result.Items))
	}
	if string(result.Items[0].State) != "running" {
		t.Fatalf("container state: got %s, want running", result.Items[0].State)
	}
	return result.Items[0].ID
}

// ensureImage 预拉取测试镜像，避免 worker timeout 被 registry 延迟主导。
func (h *dockerHarness) ensureImage(t *testing.T, image string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := h.client.ImageInspect(ctx, image); err == nil {
		return
	}
	stream, err := h.client.ImagePull(
		ctx,
		image,
		mobyclient.ImagePullOptions{},
	)
	if err != nil {
		t.Fatalf("pull integration image")
	}
	waitErr := stream.Wait(ctx)
	closeErr := stream.Close()
	if waitErr != nil || closeErr != nil {
		t.Fatalf("consume integration image pull")
	}
}

// reserveLoopbackAddress 取得当前可绑定的 loopback 地址。
func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve sandboxd address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release sandboxd address: %v", err)
	}
	return address
}

// integrationImage 返回显式测试镜像或 Phase 1 默认镜像。
func integrationImage() string {
	if image := os.Getenv(integrationImageEnv); image != "" {
		return image
	}
	return "debian:bookworm-slim"
}

// integrationConfig 生成只绑定 loopback、网络为 none 的测试配置。
func integrationConfig(dataDirectory, address, dockerHost string) string {
	runRoot := filepath.Join(dataDirectory, "run")
	workspaceRoot := filepath.Join(dataDirectory, "workspaces")
	databasePath := filepath.Join(dataDirectory, "sandboxd.db")
	return fmt.Sprintf(`server:
  listen_address: %s
  shutdown_timeout: "10s"
data:
  directory: %s
  sqlite_path: %s
runtime:
  type: "docker"
  docker_host: %s
  default_image: "debian:bookworm-slim"
  runner_socket_directory: %s
  workspace_directory: %s
  network_mode: "none"
  workspace_persistent: false
  platform:
    os: "linux"
    arch: "amd64"
reconcile:
  interval: "250ms"
  runner_ready_timeout: "30s"
  deletion_timeout: "2m"
limits:
  default_ttl: "30m"
  maximum_ttl: "24h"
  default_resources:
    cpu_quota_millis: 500
    memory_mib: 256
    pids: 64
  max_resources:
    cpu_quota_millis: 4000
    memory_mib: 8192
    pids: 1024
`,
		strconv.Quote(address),
		strconv.Quote(dataDirectory),
		strconv.Quote(databasePath),
		strconv.Quote(dockerHost),
		strconv.Quote(runRoot),
		strconv.Quote(workspaceRoot),
	)
}
