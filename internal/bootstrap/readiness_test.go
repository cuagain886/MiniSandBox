package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"testing"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/config"
	"minisandbox/internal/datadir"
	"minisandbox/internal/reconcile"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/internal/store"
)

// TestRunAdvancesReadinessAfterEachSuccessfulStage 验证启动位严格按依赖顺序推进。
func TestRunAdvancesReadinessAfterEachSuccessfulStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make([]string, 0, 12)
	var readiness *controlapi.Readiness
	factory := recordingFactories(&events, cancel, "", nil)
	factory.readiness = func() *controlapi.Readiness {
		readiness = &controlapi.Readiness{}
		return readiness
	}

	originalArtifacts := factory.artifacts
	factory.artifacts = func() (dockerruntime.ArtifactProvider, error) {
		assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{Store: true})
		return originalArtifacts()
	}
	originalRuntime := factory.openRuntime
	factory.openRuntime = func(
		ctx context.Context,
		cfg config.Config,
		paths datadir.Paths,
		artifacts dockerruntime.ArtifactProvider,
	) (managedRuntime, error) {
		assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{
			Store:    true,
			Artifact: true,
		})
		return originalRuntime(ctx, cfg, paths, artifacts)
	}
	originalWorker := factory.startWorker
	factory.startWorker = func(
		ctx context.Context,
		cfg config.Config,
		paths datadir.Paths,
		sandboxStore store.Store,
		runtime runtimeport.Runtime,
		queue *reconcile.WakeQueue,
	) (workerHandle, error) {
		assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{
			Store:    true,
			Docker:   true,
			Artifact: true,
		})
		return originalWorker(
			ctx,
			cfg,
			paths,
			sandboxStore,
			runtime,
			queue,
		)
	}
	originalRecovery := factory.recover
	factory.recover = func(
		ctx context.Context,
		sandboxStore store.Store,
		runtime runtimeport.Runtime,
		queue *reconcile.WakeQueue,
		readiness *controlapi.Readiness,
	) error {
		assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{
			Store:    true,
			Docker:   true,
			Artifact: true,
			Worker:   true,
		})
		return originalRecovery(
			ctx,
			sandboxStore,
			runtime,
			queue,
			readiness,
		)
	}
	originalHTTP := factory.startHTTP
	factory.startHTTP = func(
		cfg config.Config,
		build controlapi.BuildInfo,
		sandboxStore store.Store,
		runtime runtimeport.Runtime,
		queue *reconcile.WakeQueue,
		readiness *controlapi.Readiness,
	) (httpHandle, error) {
		snapshot := readiness.Snapshot()
		if !snapshot.Ready() {
			t.Fatalf("HTTP started before ready: %#v", snapshot)
		}
		return originalHTTP(
			cfg,
			build,
			sandboxStore,
			runtime,
			queue,
			readiness,
		)
	}

	if err := run(ctx, Options{ConfigPath: "test.yaml"}, factory); err != nil {
		t.Fatalf("run bootstrap: %v", err)
	}
	assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{})
}

// TestRunStartupFailuresLeaveReadinessFalse 验证任一阶段失败都不会遗留 ready 位。
func TestRunStartupFailuresLeaveReadinessFalse(t *testing.T) {
	cause := errors.New("startup failure")
	for _, failAt := range []string{
		"config",
		"directories",
		"store",
		"artifacts",
		"runtime",
		"worker",
		"recovery",
		"maintenance",
		"http",
	} {
		t.Run(failAt, func(t *testing.T) {
			events := make([]string, 0, 12)
			var readiness *controlapi.Readiness
			factory := recordingFactories(
				&events,
				func() {},
				failAt,
				cause,
			)
			factory.readiness = func() *controlapi.Readiness {
				readiness = &controlapi.Readiness{}
				return readiness
			}

			err := run(
				context.Background(),
				Options{ConfigPath: "test.yaml"},
				factory,
			)
			if !errors.Is(err, cause) {
				t.Fatalf("startup error: %v", err)
			}
			assertSnapshot(t, readiness, controlapi.ReadinessSnapshot{})
		})
	}
}

// TestRunMarksNotReadyBeforeHTTPShutdown 验证关闭监听前已撤销 readiness。
func TestRunMarksNotReadyBeforeHTTPShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make([]string, 0, 12)
	var readiness *controlapi.Readiness
	factory := recordingFactories(&events, cancel, "", nil)
	factory.readiness = func() *controlapi.Readiness {
		readiness = &controlapi.Readiness{}
		return readiness
	}
	originalHTTP := factory.startHTTP
	factory.startHTTP = func(
		cfg config.Config,
		build controlapi.BuildInfo,
		sandboxStore store.Store,
		runtime runtimeport.Runtime,
		queue *reconcile.WakeQueue,
		readiness *controlapi.Readiness,
	) (httpHandle, error) {
		handle, err := originalHTTP(
			cfg,
			build,
			sandboxStore,
			runtime,
			queue,
			readiness,
		)
		if err != nil {
			return nil, err
		}
		return &readinessHTTP{
			httpHandle: handle,
			readiness:  readiness,
			t:          t,
		}, nil
	}

	if err := run(ctx, Options{ConfigPath: "test.yaml"}, factory); err != nil {
		t.Fatalf("run bootstrap: %v", err)
	}
}

// readinessHTTP 在关闭时断言服务已经 not-ready。
type readinessHTTP struct {
	httpHandle
	readiness *controlapi.Readiness
	t         *testing.T
}

// Close 验证 readiness 后委托底层测试 handle。
func (h *readinessHTTP) Close(ctx context.Context) error {
	h.t.Helper()
	if h.readiness.Snapshot().Ready() {
		h.t.Fatal("HTTP shutdown began while service was still ready")
	}
	return h.httpHandle.Close(ctx)
}

// assertSnapshot 比较完整 readiness 快照。
func assertSnapshot(
	t *testing.T,
	readiness *controlapi.Readiness,
	want controlapi.ReadinessSnapshot,
) {
	t.Helper()
	if got := readiness.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("readiness: got %#v, want %#v", got, want)
	}
}
