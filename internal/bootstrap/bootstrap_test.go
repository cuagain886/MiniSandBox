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
	"minisandbox/internal/testutil"
)

// TestRunStartsAndClosesDependenciesInOrder 验证成功启动和逆序关闭。
func TestRunStartsAndClosesDependenciesInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make([]string, 0, 12)
	factory := recordingFactories(&events, cancel, "", nil)

	if err := run(ctx, Options{ConfigPath: "test.yaml"}, factory); err != nil {
		t.Fatalf("run bootstrap: %v", err)
	}
	want := []string{
		"config",
		"directories",
		"store",
		"artifacts",
		"runtime",
		"worker",
		"recovery",
		"maintenance",
		"http",
		"http-close",
		"maintenance-close",
		"worker-close",
		"runtime-close",
		"store-close",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events:\n got %v\nwant %v", events, want)
	}
}

// TestRunFailureClosesOnlyStartedDependencies 验证各失败点不会泄露后续组件。
func TestRunFailureClosesOnlyStartedDependencies(t *testing.T) {
	cause := errors.New("injected startup failure")
	tests := []struct {
		name       string
		failAt     string
		wantEvents []string
	}{
		{
			name:   "artifacts",
			failAt: "artifacts",
			wantEvents: []string{
				"config",
				"directories",
				"store",
				"artifacts",
				"store-close",
			},
		},
		{
			name:   "recovery",
			failAt: "recovery",
			wantEvents: []string{
				"config",
				"directories",
				"store",
				"artifacts",
				"runtime",
				"worker",
				"recovery",
				"worker-close",
				"runtime-close",
				"store-close",
			},
		},
		{
			name:   "maintenance",
			failAt: "maintenance",
			wantEvents: []string{
				"config", "directories", "store", "artifacts", "runtime", "worker", "recovery", "maintenance",
				"worker-close", "runtime-close", "store-close",
			},
		},
		{
			name:   "http",
			failAt: "http",
			wantEvents: []string{
				"config",
				"directories",
				"store",
				"artifacts",
				"runtime",
				"worker",
				"recovery",
				"maintenance",
				"http",
				"maintenance-close",
				"worker-close",
				"runtime-close",
				"store-close",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make([]string, 0, 12)
			factory := recordingFactories(
				&events,
				func() {},
				tt.failAt,
				cause,
			)
			err := run(
				context.Background(),
				Options{ConfigPath: "test.yaml"},
				factory,
			)
			if !errors.Is(err, cause) {
				t.Fatalf("startup error: %v", err)
			}
			if !reflect.DeepEqual(events, tt.wantEvents) {
				t.Fatalf("events:\n got %v\nwant %v", events, tt.wantEvents)
			}
		})
	}
}

// recordingFactories 创建记录启动、失败和关闭顺序的依赖替身。
func recordingFactories(
	events *[]string,
	cancel context.CancelFunc,
	failAt string,
	cause error,
) factories {
	sandboxStore := &recordingStore{
		FakeStore: testutil.NewFakeStore(),
		events:    events,
	}
	runtime := &recordingRuntime{
		FakeRuntime: testutil.NewFakeRuntime(),
		events:      events,
	}
	return factories{
		readiness: func() *controlapi.Readiness {
			return &controlapi.Readiness{}
		},
		loadConfig: func(string) (config.Config, error) {
			*events = append(*events, "config")
			return config.Default(), stageError("config", failAt, cause)
		},
		directories: func(config.Config) (datadir.Paths, error) {
			*events = append(*events, "directories")
			return datadir.Paths{}, stageError("directories", failAt, cause)
		},
		openStore: func(
			context.Context,
			datadir.Paths,
		) (managedStore, error) {
			*events = append(*events, "store")
			if err := stageError("store", failAt, cause); err != nil {
				return nil, err
			}
			return sandboxStore, nil
		},
		artifacts: func() (dockerruntime.ArtifactProvider, error) {
			*events = append(*events, "artifacts")
			if err := stageError("artifacts", failAt, cause); err != nil {
				return nil, err
			}
			return emptyArtifactProvider{}, nil
		},
		openRuntime: func(
			context.Context,
			config.Config,
			datadir.Paths,
			dockerruntime.ArtifactProvider,
		) (managedRuntime, error) {
			*events = append(*events, "runtime")
			if err := stageError("runtime", failAt, cause); err != nil {
				return nil, err
			}
			return runtime, nil
		},
		startWorker: func(
			context.Context,
			config.Config,
			datadir.Paths,
			store.Store,
			runtimeport.Runtime,
			*reconcile.WakeQueue,
		) (workerHandle, error) {
			*events = append(*events, "worker")
			if err := stageError("worker", failAt, cause); err != nil {
				return nil, err
			}
			return &recordingWorker{events: events, closeEvent: "worker-close"}, nil
		},
		recover: func(
			context.Context,
			store.Store,
			runtimeport.Runtime,
			*reconcile.WakeQueue,
			*controlapi.Readiness,
		) error {
			*events = append(*events, "recovery")
			return stageError("recovery", failAt, cause)
		},
		startMaintenance: func(context.Context, config.Config, store.Store, runtimeport.Runtime, *reconcile.WakeQueue, *controlapi.Readiness) (workerHandle, error) {
			*events = append(*events, "maintenance")
			if err := stageError("maintenance", failAt, cause); err != nil {
				return nil, err
			}
			return &recordingWorker{events: events, closeEvent: "maintenance-close"}, nil
		},
		startHTTP: func(
			config.Config,
			controlapi.BuildInfo,
			store.Store,
			runtimeport.Runtime,
			*reconcile.WakeQueue,
			*controlapi.Readiness,
		) (httpHandle, error) {
			*events = append(*events, "http")
			if err := stageError("http", failAt, cause); err != nil {
				return nil, err
			}
			cancel()
			return &recordingHTTP{
				events: events,
				done:   make(chan error),
			}, nil
		},
	}
}

// stageError 在指定测试阶段返回注入错误。
func stageError(stage, failAt string, cause error) error {
	if stage == failAt {
		return cause
	}
	return nil
}

// recordingStore 为 Store fake 增加关闭记录。
type recordingStore struct {
	*testutil.FakeStore
	events *[]string
}

// Close 记录 Store 关闭。
func (s *recordingStore) Close() error {
	*s.events = append(*s.events, "store-close")
	return nil
}

// recordingRuntime 为 Runtime fake 增加关闭记录。
type recordingRuntime struct {
	*testutil.FakeRuntime
	events *[]string
}

// Close 记录 runtime 关闭。
func (r *recordingRuntime) Close() error {
	*r.events = append(*r.events, "runtime-close")
	return nil
}

// recordingWorker 记录 worker 关闭。
type recordingWorker struct {
	events     *[]string
	closeEvent string
}

// Close 记录 worker 已等待退出。
func (w *recordingWorker) Close(context.Context) error {
	*w.events = append(*w.events, w.closeEvent)
	return nil
}

// recordingHTTP 记录 HTTP 关闭。
type recordingHTTP struct {
	events *[]string
	done   chan error
}

// Done 返回不会自行结束的测试 server channel。
func (s *recordingHTTP) Done() <-chan error {
	return s.done
}

// Close 记录 HTTP server 关闭。
func (s *recordingHTTP) Close(context.Context) error {
	*s.events = append(*s.events, "http-close")
	return nil
}

// emptyArtifactProvider 是只用于跳过真实 ELF 的装配替身。
type emptyArtifactProvider struct{}

// Artifacts 返回空测试集合，fake runtime 不读取该值。
func (emptyArtifactProvider) Artifacts() dockerruntime.ArtifactSet {
	return dockerruntime.ArtifactSet{}
}

var _ managedStore = (*recordingStore)(nil)
var _ managedRuntime = (*recordingRuntime)(nil)
