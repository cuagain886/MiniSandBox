//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	mobyclient "github.com/moby/moby/client"
)

const executionHelperPath = "/usr/local/bin/minisandbox-execution-helper"

// buildExecutionHelper 构建与测试宿主同架构的静态 Linux 探针，避免依赖测试镜像内的调试工具。
func buildExecutionHelper(t *testing.T) []byte {
	t.Helper()
	output := filepath.Join(t.TempDir(), "minisandbox-execution-helper")
	command := exec.Command("go", "build", "-trimpath", "-o", output, "./tests/integration/fixtures/execution-helper")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if content, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build execution helper: %v: %s", err, content)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read execution helper: %v", err)
	}
	return data
}

// installExecutionHelper 把测试专用探针注入当前 sandbox，不改变镜像或生产 artifact。
func installExecutionHelper(t *testing.T, client *mobyclient.Client, containerID string, helper []byte) {
	t.Helper()
	archive := executableArchive(t, map[string][]byte{"usr/local/bin/minisandbox-execution-helper": helper})
	if _, err := client.CopyToContainer(context.Background(), containerID, mobyclient.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         io.NopCloser(bytes.NewReader(archive)),
	}); err != nil {
		t.Fatalf("inject execution helper")
	}
}

// registerBackgroundLogOwnershipCleanup 在容器销毁前临时开放本测试日志目录，避免 WSL bind mount 的 UID 阻止临时目录清理。
func registerBackgroundLogOwnershipCleanup(t *testing.T, client *mobyclient.Client, containerID string) {
	t.Helper()
	owner := strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid())
	t.Cleanup(func() {
		if code := execAndWait(t, client, containerID, owner, []string{"/bin/chmod", "0711", "/run/minisandbox"}); code != 0 {
			t.Errorf("open runtime traversal for cleanup: exit=%d", code)
			return
		}
		if code := execAndWait(t, client, containerID, "65532:65532", []string{"/bin/chmod", "-R", "0777", "/run/minisandbox/executions"}); code != 0 {
			t.Errorf("open background logs for cleanup: exit=%d", code)
		}
		if code := execAndWait(t, client, containerID, owner, []string{"/bin/chmod", "0700", "/run/minisandbox"}); code != 0 {
			t.Errorf("restore runtime directory mode: exit=%d", code)
		}
	})
}
