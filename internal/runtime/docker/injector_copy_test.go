package docker

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
)

// TestCopyArtifactsUsesFixedDestinationAndTar 验证 Copy API 只收到两个固定 artifact。
func TestCopyArtifactsUsesFixedDestinationAndTar(t *testing.T) {
	var names []string
	engine := &fakeEngine{
		copyToContainerFunc: func(
			_ context.Context,
			containerID string,
			options mobyclient.CopyToContainerOptions,
		) (mobyclient.CopyToContainerResult, error) {
			if containerID != "container-id" {
				t.Fatalf("container ID: got %q", containerID)
			}
			if options.DestinationPath != artifactDirectory ||
				options.AllowOverwriteDirWithFile ||
				options.CopyUIDGID {
				t.Fatalf("copy options: %#v", options)
			}
			reader := tar.NewReader(options.Content)
			for {
				header, err := reader.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("read artifact tar: %v", err)
				}
				names = append(names, header.Name)
				if _, err := io.Copy(io.Discard, reader); err != nil {
					t.Fatalf("consume artifact: %v", err)
				}
			}
			return mobyclient.CopyToContainerResult{}, nil
		},
	}

	err := copyArtifacts(
		context.Background(),
		engine,
		"container-id",
		testArtifactProvider(),
		time.Second,
	)
	if err != nil {
		t.Fatalf("copy artifacts: %v", err)
	}
	if !reflect.DeepEqual(names, []string{RunnerArtifactName, InitArtifactName}) {
		t.Fatalf("tar entries: %v", names)
	}
}

// TestCopyArtifactsPropagatesEngineFailure 验证 copy 故障保留 cause 但不泄露详情。
func TestCopyArtifactsPropagatesEngineFailure(t *testing.T) {
	cause := errors.New("copy daemon secret detail")
	engine := &fakeEngine{
		copyToContainerFunc: func(
			context.Context,
			string,
			mobyclient.CopyToContainerOptions,
		) (mobyclient.CopyToContainerResult, error) {
			return mobyclient.CopyToContainerResult{}, cause
		},
	}

	err := copyArtifacts(
		context.Background(),
		engine,
		"container-id",
		testArtifactProvider(),
		time.Second,
	)
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, cause) {
		t.Fatalf("error: got %T %v", err, err)
	}
	if strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("copy error exposed daemon detail")
	}
}

// TestCopyArtifactsHonorsTimeout 验证 copy 使用独立 deadline 并传播取消。
func TestCopyArtifactsHonorsTimeout(t *testing.T) {
	engine := &fakeEngine{
		copyToContainerFunc: func(
			ctx context.Context,
			_ string,
			_ mobyclient.CopyToContainerOptions,
		) (mobyclient.CopyToContainerResult, error) {
			<-ctx.Done()
			return mobyclient.CopyToContainerResult{}, ctx.Err()
		},
	}

	err := copyArtifacts(
		context.Background(),
		engine,
		"container-id",
		testArtifactProvider(),
		10*time.Millisecond,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error: got %v, want deadline exceeded", err)
	}
}

// TestCopyArtifactsHonorsParentCancellation 验证调用方取消会传递到 Docker Copy API。
func TestCopyArtifactsHonorsParentCancellation(t *testing.T) {
	engine := &fakeEngine{
		copyToContainerFunc: func(
			ctx context.Context,
			_ string,
			_ mobyclient.CopyToContainerOptions,
		) (mobyclient.CopyToContainerResult, error) {
			<-ctx.Done()
			return mobyclient.CopyToContainerResult{}, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := copyArtifacts(
		ctx,
		engine,
		"container-id",
		testArtifactProvider(),
		time.Second,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: got %v, want canceled", err)
	}
}

// testArtifactProvider 返回包含两个合法测试 ELF 的 provider。
func testArtifactProvider() ArtifactProvider {
	executable := testELF64AMD64()
	return staticArtifactProvider{
		artifacts: ArtifactSet{
			Runner: Artifact{Name: RunnerArtifactName, Data: executable},
			Init:   Artifact{Name: InitArtifactName, Data: executable},
		},
	}
}
