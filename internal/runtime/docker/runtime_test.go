package docker

import (
	"context"
	"errors"
	"io"
	"iter"
	"testing"

	"github.com/moby/moby/api/types/jsonstream"
	mobyclient "github.com/moby/moby/client"
)

// fakeEngine 是 P1-036 只实现 Ping/Close 的窄 Docker client 替身。
type fakeEngine struct {
	pingResult  mobyclient.PingResult
	pingErr     error
	pingCalls   int
	pingOptions []mobyclient.PingOptions
	closeCalls  int
	closeErr    error

	imageInspectFunc func(
		context.Context,
		string,
		...mobyclient.ImageInspectOption,
	) (mobyclient.ImageInspectResult, error)
	imagePullFunc func(
		context.Context,
		string,
		mobyclient.ImagePullOptions,
	) (mobyclient.ImagePullResponse, error)
	containerInspectFunc func(
		context.Context,
		string,
		mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error)
	containerCreateFunc func(
		context.Context,
		mobyclient.ContainerCreateOptions,
	) (mobyclient.ContainerCreateResult, error)
	copyToContainerFunc func(
		context.Context,
		string,
		mobyclient.CopyToContainerOptions,
	) (mobyclient.CopyToContainerResult, error)
	containerStartFunc func(
		context.Context,
		string,
		mobyclient.ContainerStartOptions,
	) (mobyclient.ContainerStartResult, error)
	volumeInspectFunc func(
		context.Context,
		string,
		mobyclient.VolumeInspectOptions,
	) (mobyclient.VolumeInspectResult, error)
	volumeCreateFunc func(
		context.Context,
		mobyclient.VolumeCreateOptions,
	) (mobyclient.VolumeCreateResult, error)
}

// Ping 记录版本协商选项并返回预设结果。
func (f *fakeEngine) Ping(
	_ context.Context,
	options mobyclient.PingOptions,
) (mobyclient.PingResult, error) {
	f.pingCalls++
	f.pingOptions = append(f.pingOptions, options)
	return f.pingResult, f.pingErr
}

// Close 记录资源释放并返回预设错误。
func (f *fakeEngine) Close() error {
	f.closeCalls++
	return f.closeErr
}

// ImageInspect 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) ImageInspect(
	ctx context.Context,
	image string,
	options ...mobyclient.ImageInspectOption,
) (mobyclient.ImageInspectResult, error) {
	if f.imageInspectFunc == nil {
		return mobyclient.ImageInspectResult{}, nil
	}
	return f.imageInspectFunc(ctx, image, options...)
}

// ImagePull 调用测试注入函数；未配置时返回空成功流。
func (f *fakeEngine) ImagePull(
	ctx context.Context,
	image string,
	options mobyclient.ImagePullOptions,
) (mobyclient.ImagePullResponse, error) {
	if f.imagePullFunc == nil {
		return newFakePullResponse(nil), nil
	}
	return f.imagePullFunc(ctx, image, options)
}

// ContainerInspect 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) ContainerInspect(
	ctx context.Context,
	name string,
	options mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	if f.containerInspectFunc == nil {
		return mobyclient.ContainerInspectResult{}, nil
	}
	return f.containerInspectFunc(ctx, name, options)
}

// ContainerCreate 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) ContainerCreate(
	ctx context.Context,
	options mobyclient.ContainerCreateOptions,
) (mobyclient.ContainerCreateResult, error) {
	if f.containerCreateFunc == nil {
		return mobyclient.ContainerCreateResult{}, nil
	}
	return f.containerCreateFunc(ctx, options)
}

// CopyToContainer 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) CopyToContainer(
	ctx context.Context,
	containerID string,
	options mobyclient.CopyToContainerOptions,
) (mobyclient.CopyToContainerResult, error) {
	if f.copyToContainerFunc == nil {
		return mobyclient.CopyToContainerResult{}, nil
	}
	return f.copyToContainerFunc(ctx, containerID, options)
}

// ContainerStart 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) ContainerStart(
	ctx context.Context,
	containerID string,
	options mobyclient.ContainerStartOptions,
) (mobyclient.ContainerStartResult, error) {
	if f.containerStartFunc == nil {
		return mobyclient.ContainerStartResult{}, nil
	}
	return f.containerStartFunc(ctx, containerID, options)
}

// VolumeInspect 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) VolumeInspect(
	ctx context.Context,
	name string,
	options mobyclient.VolumeInspectOptions,
) (mobyclient.VolumeInspectResult, error) {
	if f.volumeInspectFunc == nil {
		return mobyclient.VolumeInspectResult{}, nil
	}
	return f.volumeInspectFunc(ctx, name, options)
}

// VolumeCreate 调用测试注入函数；未配置时返回零值成功。
func (f *fakeEngine) VolumeCreate(
	ctx context.Context,
	options mobyclient.VolumeCreateOptions,
) (mobyclient.VolumeCreateResult, error) {
	if f.volumeCreateFunc == nil {
		return mobyclient.VolumeCreateResult{}, nil
	}
	return f.volumeCreateFunc(ctx, options)
}

// fakePullResponse 模拟必须 Wait 并 Close 的 Docker pull stream。
type fakePullResponse struct {
	waitErr    error
	waitCalls  int
	closeErr   error
	closeCalls int
}

// newFakePullResponse 创建返回指定 Wait 错误的响应流。
func newFakePullResponse(waitErr error) *fakePullResponse {
	return &fakePullResponse{waitErr: waitErr}
}

// Read 为 io.ReadCloser 兼容面返回 EOF；测试通过 Wait 记录完整消费。
func (r *fakePullResponse) Read([]byte) (int, error) {
	return 0, io.EOF
}

// Close 记录流关闭次数。
func (r *fakePullResponse) Close() error {
	r.closeCalls++
	return r.closeErr
}

// Wait 记录完整消费并返回预设 pull 结果。
func (r *fakePullResponse) Wait(context.Context) error {
	r.waitCalls++
	return r.waitErr
}

// JSONMessages 返回空序列；生产代码使用 Wait 完整消费。
func (r *fakePullResponse) JSONMessages(
	context.Context,
) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

// TestNewRuntimePingsWithVersionNegotiation 验证构造阶段探测并协商 API。
func TestNewRuntimePingsWithVersionNegotiation(t *testing.T) {
	engine := &fakeEngine{
		pingResult: mobyclient.PingResult{APIVersion: "1.55"},
	}

	runtime, err := newRuntime(context.Background(), engine)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if runtime.engine != engine {
		t.Fatal("runtime did not retain injected engine")
	}
	if engine.pingCalls != 1 ||
		len(engine.pingOptions) != 1 ||
		!engine.pingOptions[0].NegotiateAPIVersion {
		t.Fatalf("ping calls/options: %d %#v", engine.pingCalls, engine.pingOptions)
	}
	if engine.closeCalls != 0 {
		t.Fatalf("successful constructor closed engine %d times", engine.closeCalls)
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if engine.closeCalls != 1 {
		t.Fatalf("close calls: got %d, want 1", engine.closeCalls)
	}
}

// TestNewRuntimePingFailureIsUnavailable 验证失败保留 cause、关闭 client 并可映射 503。
func TestNewRuntimePingFailureIsUnavailable(t *testing.T) {
	cause := errors.New("secret docker socket path")
	engine := &fakeEngine{pingErr: cause}

	runtime, err := newRuntime(context.Background(), engine)
	if runtime != nil {
		t.Fatalf("runtime on failure: %#v", runtime)
	}
	if err == nil {
		t.Fatal("expected ping error")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error lost cause: %v", err)
	}
	var unavailable interface{ Unavailable() bool }
	if !errors.As(err, &unavailable) || !unavailable.Unavailable() {
		t.Fatalf("error is not unavailable: %T %v", err, err)
	}
	if err.Error() == cause.Error() {
		t.Fatal("public-facing error text must not expose the cause")
	}
	if engine.closeCalls != 1 {
		t.Fatalf("failed constructor close calls: got %d, want 1", engine.closeCalls)
	}
}

// TestNewRuntimeRejectsNilEngine 验证装配错误不会触发 nil panic。
func TestNewRuntimeRejectsNilEngine(t *testing.T) {
	runtime, err := newRuntime(context.Background(), nil)
	if err == nil || runtime != nil {
		t.Fatalf("nil engine result: runtime=%#v err=%v", runtime, err)
	}
}
