package docker

import (
	"context"
	"errors"
	"testing"

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
