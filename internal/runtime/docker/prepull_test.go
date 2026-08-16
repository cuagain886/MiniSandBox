package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// TestPrepareImageUsesConfiguredPlatform 验证预拉取平台会传入 Docker pull。
func TestPrepareImageUsesConfiguredPlatform(t *testing.T) {
	inspectCalls := 0
	var pullOptions mobyclient.ImagePullOptions
	engine := &fakeEngine{
		imageInspectFunc: func(context.Context, string, ...mobyclient.ImageInspectOption) (mobyclient.ImageInspectResult, error) {
			inspectCalls++
			if inspectCalls == 1 {
				return mobyclient.ImageInspectResult{}, cerrdefs.ErrNotFound
			}
			return mobyclient.ImageInspectResult{InspectResponse: imageInspectResponse("sha256:prepared")}, nil
		},
		imagePullFunc: func(_ context.Context, _ string, options mobyclient.ImagePullOptions) (mobyclient.ImagePullResponse, error) {
			pullOptions = options
			return newFakePullResponse(nil), nil
		},
	}
	runtime := &Runtime{engine: engine, createTimeout: time.Second}

	if err := runtime.PrepareImage(context.Background(), "alpine:3.22", "linux/amd64"); err != nil {
		t.Fatalf("prepare image: %v", err)
	}
	if len(pullOptions.Platforms) != 1 || pullOptions.Platforms[0].OS != "linux" || pullOptions.Platforms[0].Architecture != "amd64" {
		t.Fatalf("pull platforms: %#v", pullOptions.Platforms)
	}
}

// TestPrepareImageRejectsUnsupportedPlatform 验证不兼容平台在访问 Docker 前失败。
func TestPrepareImageRejectsUnsupportedPlatform(t *testing.T) {
	runtime := &Runtime{engine: &fakeEngine{}, createTimeout: time.Second}
	err := runtime.PrepareImage(context.Background(), "alpine:3.22", "linux/arm64")
	var artifactErr *ArtifactInvalidError
	if !errors.As(err, &artifactErr) {
		t.Fatalf("error type: %T, want ArtifactInvalidError", err)
	}
}
