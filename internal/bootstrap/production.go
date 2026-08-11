package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/application"
	"minisandbox/internal/config"
	"minisandbox/internal/datadir"
	"minisandbox/internal/domain"
	"minisandbox/internal/reconcile"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	"minisandbox/internal/runnerstage"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/internal/store"
	sqlitestore "minisandbox/internal/store/sqlite"
)

// productionFactories 返回只使用仓库真实 adapter 的启动依赖。
func productionFactories() factories {
	return factories{
		readiness: func() *controlapi.Readiness {
			return &controlapi.Readiness{}
		},
		loadConfig: config.Load,
		directories: func(cfg config.Config) (datadir.Paths, error) {
			return datadir.Ensure(
				cfg.Data.Directory,
				cfg.Data.SQLitePath,
				cfg.Runtime.RunnerSocketDirectory,
			)
		},
		openStore: openProductionStore,
		artifacts: func() (dockerruntime.ArtifactProvider, error) {
			return dockerruntime.NewEmbeddedArtifactProvider()
		},
		openRuntime: openProductionRuntime,
		startWorker: startProductionWorker,
		recover:     recoverProductionState,
		startHTTP:   startProductionHTTP,
	}
}

// openProductionStore 打开并迁移 SQLite，迁移失败时立即关闭连接。
func openProductionStore(
	ctx context.Context,
	paths datadir.Paths,
) (managedStore, error) {
	sandboxStore, err := sqlitestore.Open(paths.DatabasePath)
	if err != nil {
		return nil, err
	}
	if err := sandboxStore.Migrate(ctx); err != nil {
		return nil, errors.Join(err, sandboxStore.Close())
	}
	return sandboxStore, nil
}

// openProductionRuntime 创建并 Ping Docker adapter。
func openProductionRuntime(
	ctx context.Context,
	cfg config.Config,
	paths datadir.Paths,
	artifacts dockerruntime.ArtifactProvider,
) (managedRuntime, error) {
	stager, err := runnerstage.New(cfg)
	if err != nil {
		return nil, err
	}
	var egress *dockerruntime.EgressPlatformConfig
	if cfg.Security.AllowOutbound {
		egress = &dockerruntime.EgressPlatformConfig{
			Image: cfg.Egress.Image, AdditionalDeniedCIDRs: append([]string(nil), cfg.Egress.DeniedCIDRs...),
			AnchorUID: cfg.Egress.AnchorUID, AnchorGID: cfg.Egress.AnchorGID,
			Limits: cfg.Egress.Limits, ReadyTimeout: cfg.Egress.ReadyTimeout,
		}
	}
	runtime, err := dockerruntime.New(
		ctx,
		cfg.Runtime.DockerHost,
		dockerruntime.RuntimeOptions{
			DataDirectory: paths.DataDirectory,
			Artifacts:     artifacts,
			CreateTimeout: defaultRuntimeCreateTimeout,
			Egress:        egress,
			Bootstrap:     stager,
		},
	)
	if err != nil {
		return nil, errors.Join(err, stager.Close())
	}
	return runtime, nil
}

// startProductionWorker 装配 runner probe、reconciler 和固定 worker pool。
func startProductionWorker(
	ctx context.Context,
	cfg config.Config,
	paths datadir.Paths,
	sandboxStore store.Store,
	runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue,
) (workerHandle, error) {
	masterKey, err := runnerauth.LoadMasterKey(cfg.Security.RunnerMasterKeyFile)
	if err != nil {
		return nil, err
	}
	factory, err := runnerclient.NewFactory(paths.RunRoot, &masterKey, runnerbootstrap.CurrentProtocolVersion, cfg.Reconcile.RunnerReadyTimeout)
	masterKey.Clear()
	if err != nil {
		return nil, err
	}
	reconciler, err := reconcile.NewWithShutdownRetryAndLimits(
		sandboxStore, runtime, factory, factory,
		reconcile.SystemClock{}, reconcile.CryptoRandom{}, cfg.Reconcile.RetryMin, cfg.Reconcile.RetryMax,
		reconcile.OperationLimits{
			MaxConcurrentCreates: cfg.Limits.MaxConcurrentCreates,
			MaxConcurrentDeletes: cfg.Limits.MaxConcurrentDeletes,
		},
	)
	if err != nil {
		factory.Close()
		return nil, err
	}
	worker, err := reconcile.NewWorkerPool(
		queue,
		cfg.Reconcile.MaxConcurrent,
		cfg.Reconcile.Timeout,
		reconciler.Reconcile,
		func(err error) {
			failure := runtimeport.ClassifyError(err)
			slog.Warn(
				"sandbox reconcile failed",
				"reason",
				failure.Reason,
			)
		},
	)
	if err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	handle := &runningWorker{
		cancel:       cancel,
		done:         make(chan struct{}),
		closeFactory: factory.Close,
	}
	go func() {
		defer close(handle.done)
		worker.Run(workerCtx)
	}()
	return handle, nil
}

// recoverProductionState 执行一次启动对账并只记录安全诊断码。
func recoverProductionState(
	ctx context.Context,
	sandboxStore store.Store,
	runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue,
	readiness *controlapi.Readiness,
) error {
	service, err := reconcile.NewRecoveryService(
		sandboxStore,
		runtime,
		queue,
		readiness,
		func(diagnostic reconcile.RecoveryDiagnostic) {
			slog.Warn(
				"sandbox recovery diagnostic",
				"code",
				diagnostic.Code,
				"sandbox_id",
				diagnostic.SandboxID,
			)
		},
	)
	if err != nil {
		return err
	}
	return service.Run(ctx)
}

// startProductionHTTP 绑定端口后启动真实生命周期 API。
func startProductionHTTP(
	cfg config.Config,
	build controlapi.BuildInfo,
	sandboxStore store.Store,
	runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue,
	readiness *controlapi.Readiness,
) (httpHandle, error) {
	lifecycle := application.NewSandboxServiceWithCreatePolicy(
		sandboxStore,
		application.NewRandomIDGenerator(),
		application.SystemClock{},
		application.NewSandboxSpecBuilder(
			cfg.DefaultSandboxSpec(),
			cfg.Limits.MaxResources,
		),
		queueWaker{queue: queue},
		cfg.Security.AllowOutbound,
		application.CreatePolicy{
			DefaultTTL: cfg.Limits.DefaultTTL, MinimumTTL: cfg.Limits.MinimumTTL,
			MaximumTTL: cfg.Limits.MaximumTTL, MaxSandboxes: cfg.Limits.MaxSandboxes,
		},
	)
	masterKey, err := runnerauth.LoadMasterKey(cfg.Security.RunnerMasterKeyFile)
	if err != nil {
		return nil, err
	}
	runnerFactory, err := runnerclient.NewFactory(cfg.Runtime.RunnerSocketDirectory, &masterKey, runnerbootstrap.CurrentProtocolVersion, cfg.Reconcile.RunnerReadyTimeout)
	masterKey.Clear()
	if err != nil {
		return nil, err
	}
	var execution *application.ExecutionService
	if cfg.Security.AllowOutbound {
		egressGate, ok := runtime.(runtimeport.ExecutionEgressGate)
		if !ok {
			runnerFactory.Close()
			return nil, errors.New("runtime does not provide outbound execution admission")
		}
		execution, err = application.NewExecutionServiceWithAdmissionGate(
			sandboxStore,
			applicationExecutionFactory{factory: runnerFactory},
			outboundExecutionAdmission{runtime: egressGate},
			cfg.Runner.MaxLogPageEvents,
		)
	} else {
		execution, err = application.NewExecutionService(sandboxStore, applicationExecutionFactory{factory: runnerFactory}, cfg.Runner.MaxLogPageEvents)
	}
	if err != nil {
		runnerFactory.Close()
		return nil, err
	}
	server := &http.Server{
		Addr: cfg.Server.ListenAddress,
		Handler: controlapi.NewRouter(
			build,
			controlapi.RouterDependencies{
				Lifecycle:       lifecycle,
				Execution:       execution,
				SSEWriteTimeout: cfg.Runner.SSEWriteTimeout,
				Readiness:       readiness,
			},
		),
		ReadHeaderTimeout: cfg.Server.ShutdownTimeout,
	}
	listener, err := net.Listen("tcp", cfg.Server.ListenAddress)
	if err != nil {
		runnerFactory.Close()
		return nil, err
	}
	handle := &runningHTTP{
		server:       server,
		done:         make(chan error, 1),
		closeFactory: runnerFactory.Close,
	}
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		handle.done <- err
		close(handle.done)
	}()
	return handle, nil
}

// outboundExecutionAdmission 在每次创建 outbound execution 前，把 runner 自报的 netns
// 与 runtime 从受信 Docker 状态重建的 sidecar 身份进行只读比对；任一信息不可用都拒绝准入。
type outboundExecutionAdmission struct {
	runtime runtimeport.ExecutionEgressGate
}

// Check 不接受调用方提供的容器或 sidecar 标识，避免用未受信输入选择网络命名空间。
func (gate outboundExecutionAdmission) Check(ctx context.Context, sandbox domain.Sandbox, client application.ExecutionClient) error {
	identityClient, ok := client.(application.ExecutionNetworkIdentityClient)
	if !ok || gate.runtime == nil {
		return errors.New("runner does not provide network namespace identity")
	}
	identity, err := identityClient.NetworkNamespace(ctx)
	if err != nil {
		return errors.New("read runner network namespace identity")
	}
	if err := gate.runtime.CheckSandboxEgress(ctx, sandbox.ID, identity); err != nil {
		return errors.New("outbound execution admission rejected")
	}
	return nil
}

// queueWaker 把带返回值的 WakeQueue 适配为 application 尽力通知端口。
type queueWaker struct {
	queue *reconcile.WakeQueue
}

// Wake 尝试把持久化后的 sandbox ID 放入合并队列。
func (w queueWaker) Wake(id string) {
	w.queue.Wake(id)
}

// runningWorker 管理 worker goroutine 的取消和等待。
type runningWorker struct {
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
	closeFactory func()
}

// Close 取消 worker 并等待当前 reconcile 退出。
func (w *runningWorker) Close(ctx context.Context) error {
	w.once.Do(w.cancel)
	defer func() {
		if w.closeFactory != nil {
			w.closeFactory()
		}
	}()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runningHTTP 管理已经绑定端口的 HTTP server。
type runningHTTP struct {
	server       *http.Server
	done         chan error
	once         sync.Once
	closeFactory func()
}

// Done 返回 server 退出结果。
func (s *runningHTTP) Done() <-chan error {
	return s.done
}

// Close 优雅关闭 HTTP server。
func (s *runningHTTP) Close(ctx context.Context) error {
	if s.closeFactory != nil {
		defer s.closeFactory()
	}
	var err error
	s.once.Do(func() {
		err = s.server.Shutdown(ctx)
	})
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ application.Waker = queueWaker{}
