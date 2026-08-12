//go:build integration

package integration

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"minisandbox/internal/runnerauth"
)

type crashSandboxd struct {
	command *exec.Cmd
	done    chan error
	baseURL string
	key     runnerauth.MasterKey
	runRoot string
}

// buildCrashSandboxd 构建显式带 integration tag 的专用控制面；生产 binary 不包含 IPC 实现。
func buildCrashSandboxd(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "sandboxd-crash")
	command := exec.Command("go", "build", "-tags", "integration", "-trimpath", "-o", output, "./cmd/sandboxd")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if content, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build crash sandboxd: %v: %s", err, content)
	}
	return output
}

func (h *dockerHarness) writeCrashConfig(t *testing.T, address string) (string, runnerauth.MasterKey) {
	t.Helper()
	dockerHost := os.Getenv(dockerHostEnv)
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	keyPath := filepath.Join(h.dataDirectory, "runner-master-key")
	var key runnerauth.MasterKey
	if _, err := os.Lstat(keyPath); os.IsNotExist(err) {
		if _, err := rand.Read(key[:]); err != nil {
			t.Fatal("generate runner master key")
		}
		if err := os.WriteFile(keyPath, key[:], 0o400); err != nil {
			t.Fatalf("write runner master key: %v", err)
		}
	} else {
		loaded, err := runnerauth.LoadMasterKey(keyPath)
		if err != nil {
			t.Fatalf("reuse runner master key: %v", err)
		}
		key = loaded
	}
	configPath := filepath.Join(h.dataDirectory, "sandboxd-crash.yaml")
	if err := os.WriteFile(configPath, []byte(integrationConfig(h.dataDirectory, address, dockerHost, keyPath)), 0o600); err != nil {
		t.Fatalf("write crash config: %v", err)
	}
	return configPath, key
}

func startCrashSandboxd(t *testing.T, binary, configPath, address, crashpoint, socket string, key runnerauth.MasterKey, runRoot string) *crashSandboxd {
	t.Helper()
	command := exec.Command(binary, "-config", configPath)
	command.Env = append(os.Environ(), "MINISANDBOX_TEST_CRASHPOINT="+crashpoint, "MINISANDBOX_TEST_CRASHPOINT_SOCKET="+socket)
	if err := command.Start(); err != nil {
		t.Fatalf("start crash sandboxd: %v", err)
	}
	instance := &crashSandboxd{command: command, done: make(chan error, 1), baseURL: "http://" + address, key: key, runRoot: runRoot}
	go func() { instance.done <- command.Wait() }()
	return instance
}

func (s *crashSandboxd) kill(t *testing.T) {
	t.Helper()
	if err := s.command.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL sandboxd: %v", err)
	}
	select {
	case err := <-s.done:
		if err == nil {
			t.Fatal("crash sandboxd exited successfully")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGKILL sandboxd timed out")
	}
}

func waitCrashpoint(t *testing.T, listener net.Listener, name string) {
	t.Helper()
	if err := listener.(*net.UnixListener).SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.Accept()
	if err != nil {
		t.Fatalf("wait crashpoint %s: %v", name, err)
	}
	defer connection.Close()
	got, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || got != name+"\n" {
		t.Fatalf("crashpoint notification: got=%q err=%v", got, err)
	}
}

// TestCrashHarnessKillsAndRestartsExternalSandboxd 验证命中、外部强杀及同 data dir 重启均为真实进程行为。
func TestCrashHarnessKillsAndRestartsExternalSandboxd(t *testing.T) {
	harness := newDockerHarness(t)
	if runtime.GOOS != "linux" {
		t.Skip("crash harness requires Linux signals and Unix sockets")
	}
	binary := buildCrashSandboxd(t)
	address := reserveLoopbackAddress(t)
	configPath, key := harness.writeCrashConfig(t, address)
	socket := filepath.Join(harness.dataDirectory, "crashpoint.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen crashpoint IPC: %v", err)
	}
	defer listener.Close()

	crashed := startCrashSandboxd(t, binary, configPath, address, "bootstrap.http-ready", socket, key, filepath.Join(harness.dataDirectory, "run"))
	waitCrashpoint(t, listener, "bootstrap.http-ready")
	crashed.kill(t)

	restarted := startCrashSandboxd(t, binary, configPath, address, "", "", key, filepath.Join(harness.dataDirectory, "run"))
	t.Cleanup(func() {
		if restarted.command.ProcessState == nil {
			_ = restarted.command.Process.Signal(os.Interrupt)
			select {
			case <-restarted.done:
			case <-time.After(15 * time.Second):
				_ = restarted.command.Process.Kill()
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pollCondition(ctx, 100*time.Millisecond, func() (bool, error) {
		select {
		case err := <-restarted.done:
			return false, fmt.Errorf("restarted sandboxd exited: %w", err)
		default:
		}
		response, err := http.Get(restarted.baseURL + "/readyz")
		if err != nil {
			return false, nil
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK, nil
	}); err != nil {
		t.Fatal(err)
	}
}
