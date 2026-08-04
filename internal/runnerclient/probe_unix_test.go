//go:build unix

package runnerclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
	"minisandbox/pkg/protocol"
)

// TestRunnerProbeUnixSocketStatuses 验证真实 Unix Socket 上的 200、401 和 500。
func TestRunnerProbeUnixSocketStatuses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantError  bool
	}{
		{name: "ready", statusCode: http.StatusOK},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantError: true},
		{name: "server error", statusCode: http.StatusInternalServerError, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe, shutdown := startProbeServer(t, func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if request.URL.Path != "/healthz" {
					t.Errorf("path: got %q, want /healthz", request.URL.Path)
				}
				writer.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					writeReadyHealth(t, writer)
				}
			})
			defer shutdown()

			err := probe.Probe(context.Background(), testProbeSandboxID, runnerbootstrap.CurrentProtocolVersion)
			if !tt.wantError {
				if err != nil {
					t.Fatalf("probe: %v", err)
				}
				return
			}
			var unhealthy *UnhealthyError
			if !errors.As(err, &unhealthy) ||
				unhealthy.StatusCode() != tt.statusCode {
				t.Fatalf("error: got %T %v", err, err)
			}
		})
	}
}

// TestRunnerProbeReturnsProtocolMismatch 验证版本不一致不会被归类为可重试的
// 普通 unhealthy，从而阻止 reconciler 把旧版或未来版 runner 推进到 Running。
func TestRunnerProbeReturnsProtocolMismatch(t *testing.T) {
	probe, shutdown := startProbeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(protocol.RunnerHealth{
			Status:          "ok",
			Service:         "runnerd",
			Version:         "test",
			ProtocolVersion: runnerbootstrap.CurrentProtocolVersion + 1,
			NetNSIdentity:   "linux-netns:4:99",
		})
	})
	defer shutdown()
	err := probe.Probe(context.Background(), testProbeSandboxID, runnerbootstrap.CurrentProtocolVersion)
	var mismatch *ProtocolMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error: got %T %v", err, err)
	}
}

// TestRunnerProbeUnixSocketMissing 验证不存在的 socket 会等待到 ready timeout。
func TestRunnerProbeUnixSocketMissing(t *testing.T) {
	probe, err := NewRunnerProbe(shortTempDir(t), 30*time.Millisecond, "")
	if err != nil {
		t.Fatalf("new runner probe: %v", err)
	}
	probe.retryInterval = 5 * time.Millisecond
	err = probe.Probe(context.Background(), testProbeSandboxID, runnerbootstrap.CurrentProtocolVersion)
	var timeout *TimeoutError
	var missing *SocketMissingError
	if !errors.As(err, &timeout) ||
		!errors.As(err, &missing) ||
		!errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error: got %T %v", err, err)
	}
}

// TestRunnerProbeWaitsForDelayedUnixSocket 验证容器启动竞态不会立即判定失败。
func TestRunnerProbeWaitsForDelayedUnixSocket(t *testing.T) {
	root := shortTempDir(t)
	directory := filepath.Join(root, testProbeSandboxID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	probe, err := NewRunnerProbe(root, time.Second, "")
	if err != nil {
		t.Fatalf("new runner probe: %v", err)
	}
	probe.retryInterval = 5 * time.Millisecond

	serverResult := make(chan error, 1)
	shutdown := make(chan func(), 1)
	go func() {
		time.Sleep(25 * time.Millisecond)
		listener, listenErr := net.Listen(
			"unix",
			filepath.Join(directory, runnerSocketName),
		)
		if listenErr != nil {
			serverResult <- listenErr
			return
		}
		server := &http.Server{Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writeReadyHealth(t, writer)
		})}
		shutdown <- func() {
			_ = server.Close()
			_ = listener.Close()
		}
		serverResult <- server.Serve(listener)
	}()

	if err := probe.Probe(context.Background(), testProbeSandboxID, runnerbootstrap.CurrentProtocolVersion); err != nil {
		t.Fatalf("probe delayed socket: %v", err)
	}
	stop := <-shutdown
	stop()
	if err := <-serverResult; err != nil && err != http.ErrServerClosed {
		t.Fatalf("serve delayed socket: %v", err)
	}
}

// TestRunnerProbeUnixSocketTimeout 验证超时请求被取消并分类。
func TestRunnerProbeUnixSocketTimeout(t *testing.T) {
	probe, shutdown := startProbeServer(t, func(
		http.ResponseWriter,
		*http.Request,
	) {
		time.Sleep(200 * time.Millisecond)
	})
	defer shutdown()
	probe.timeout = 10 * time.Millisecond

	err := probe.Probe(context.Background(), testProbeSandboxID, runnerbootstrap.CurrentProtocolVersion)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error: got %T %v", err, err)
	}
}

// writeReadyHealth 写出可通过严格 runnerclient 校验的 health fixture。
func writeReadyHealth(t *testing.T, writer http.ResponseWriter) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(protocol.RunnerHealth{
		Status:          "ok",
		Service:         "runnerd",
		Version:         "test",
		ProtocolVersion: runnerbootstrap.CurrentProtocolVersion,
		NetNSIdentity:   "linux-netns:4:99",
	}); err != nil {
		t.Errorf("encode health: %v", err)
	}
}

// startProbeServer 在当前 sandbox 的真实 Unix Socket 上启动测试 HTTP server。
func startProbeServer(
	t *testing.T,
	handler http.HandlerFunc,
) (*Probe, func()) {
	t.Helper()
	root := shortTempDir(t)
	directory := filepath.Join(root, testProbeSandboxID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	socketPath := filepath.Join(directory, runnerSocketName)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen Unix socket: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	probe, err := NewRunnerProbe(root, time.Second, "")
	if err != nil {
		t.Fatalf("new runner probe: %v", err)
	}
	return probe, func() {
		_ = server.Close()
		_ = listener.Close()
	}
}

// shortTempDir 避免 Linux sockaddr_un 的 108 字节上限被 Go 测试长名称耗尽。
func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "ms-")
	if err != nil {
		t.Fatalf("create short temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
