package bootstrap

import (
	"context"
	"testing"
	"time"

	"minisandbox/internal/config"
	"minisandbox/internal/domain"
	runtimeport "minisandbox/internal/runtime"
)

type recordingImagePreparer struct {
	prepared chan config.PrePullImage
}

func (r *recordingImagePreparer) Ensure(context.Context, domain.Sandbox) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, nil
}

func (r *recordingImagePreparer) Inspect(context.Context, string) (runtimeport.ActualSandbox, error) {
	return runtimeport.ActualSandbox{}, nil
}

func (r *recordingImagePreparer) Delete(context.Context, string) error { return nil }

func (r *recordingImagePreparer) ListManaged(context.Context) ([]runtimeport.ActualSandbox, error) {
	return nil, nil
}

func (r *recordingImagePreparer) PrepareImage(_ context.Context, image, platform string) error {
	r.prepared <- config.PrePullImage{Image: image, Platform: platform}
	return nil
}

// TestRunImagePrePullForwardsPlatform 验证启动预拉取不会丢弃配置的平台。
func TestRunImagePrePullForwardsPlatform(t *testing.T) {
	runtime := &recordingImagePreparer{prepared: make(chan config.PrePullImage, 1)}
	want := config.PrePullImage{Image: "alpine:3.22", Platform: "linux/amd64"}
	runImagePrePull(runtime, []config.PrePullImage{want}, time.Second)

	select {
	case got := <-runtime.prepared:
		if got != want {
			t.Fatalf("prepared entry: got %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for image pre-pull")
	}
}
