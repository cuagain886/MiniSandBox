//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	dockerruntime "minisandbox/internal/runtime/docker"
)

// TestSandboxInitReapsOrphansInContainer 以打包后的 sandbox-init 作为真实容器 PID 1 验证孤儿回收。
func TestSandboxInitReapsOrphansInContainer(t *testing.T) {
	harness := newDockerHarness(t)
	image := integrationImage()
	harness.ensureImage(t, image)
	helper := buildOrphanHelper(t)
	artifacts, err := dockerruntime.NewEmbeddedArtifactProvider()
	if err != nil {
		t.Fatalf("load sandbox-init artifact: %v", err)
	}
	initBinary := artifacts.Artifacts().Init.Data
	name := "minisandbox-orphan-" + harness.testID
	created, err := harness.client.ContainerCreate(context.Background(), mobyclient.ContainerCreateOptions{
		Name:       name,
		Config:     &mobycontainer.Config{Image: image, Entrypoint: []string{"/sandbox-init", "--", "/orphan-helper"}, Labels: harness.labels()},
		HostConfig: &mobycontainer.HostConfig{NetworkMode: "none", CapDrop: []string{"ALL"}},
	})
	if err != nil {
		t.Fatalf("create orphan test container")
	}
	archive := executableArchive(t, map[string][]byte{"sandbox-init": initBinary, "orphan-helper": helper})
	if _, err := harness.client.CopyToContainer(context.Background(), created.ID, mobyclient.CopyToContainerOptions{DestinationPath: "/", Content: io.NopCloser(bytes.NewReader(archive))}); err != nil {
		t.Fatalf("inject orphan test artifacts")
	}
	if _, err := harness.client.ContainerStart(context.Background(), created.ID, mobyclient.ContainerStartOptions{}); err != nil {
		t.Fatalf("start orphan test container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pollCondition(ctx, 25*time.Millisecond, func() (bool, error) {
		result, err := harness.client.CopyFromContainer(ctx, created.ID, mobyclient.CopyFromContainerOptions{SourcePath: "/tmp/orphan-ready"})
		if err != nil {
			return false, nil
		}
		result.Content.Close()
		return true, nil
	}); err != nil {
		t.Fatalf("wait orphan helper readiness: %v", err)
	}
	top, err := harness.client.ContainerTop(ctx, created.ID, mobyclient.ContainerTopOptions{Arguments: []string{"-eo", "pid,ppid,stat,args"}})
	if err != nil {
		t.Fatalf("inspect container process table")
	}
	statIndex := -1
	for index, title := range top.Titles {
		if strings.EqualFold(title, "STAT") {
			statIndex = index
		}
	}
	if statIndex < 0 {
		t.Fatalf("process table lacks STAT column: %v", top.Titles)
	}
	for _, process := range top.Processes {
		if statIndex < len(process) && strings.HasPrefix(process[statIndex], "Z") {
			t.Fatalf("sandbox-init left zombie process: %v", process)
		}
	}
}

func buildOrphanHelper(t *testing.T) []byte {
	t.Helper()
	output := filepath.Join(t.TempDir(), "orphan-helper")
	command := exec.Command("go", "build", "-trimpath", "-o", output, "./cmd/orphan-helper")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if content, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build orphan helper: %v: %s", err, content)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read orphan helper: %v", err)
	}
	return data
}

func executableArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write artifact header: %v", err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatalf("write artifact content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close artifact archive: %v", err)
	}
	return buffer.Bytes()
}
