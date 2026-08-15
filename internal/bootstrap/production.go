package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"minisandbox/internal/adminauth"
	controlapi "minisandbox/internal/api"
	"minisandbox/internal/application"
	"minisandbox/internal/config"
	"minisandbox/internal/datadir"
	"minisandbox/internal/domain"
	"minisandbox/internal/observability/logging"
	observabilitymetrics "minisandbox/internal/observability/metrics"
	"minisandbox/internal/reconcile"
	"minisandbox/internal/runnerauth"
	"minisandbox/internal/runnerbootstrap"
	"minisandbox/internal/runnerclient"
	"minisandbox/internal/runnerstage"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/internal/store"
	sqlitestore "minisandbox/internal/store/sqlite"
	"minisandbox/internal/testcrashpoint"
)

// productionFactories 返回只使用仓库真实 adapter 的启动依赖。
func productionFactories() factories {
	registry := observabilitymetrics.NewRegistry()
	reliabilityMetrics, err := observabilitymetrics.NewReliabilityMetrics(registry)
	if err != nil {
		panic(err)
	}
	executionMetrics, err := observabilitymetrics.NewExecutionCounters(registry)
	if err != nil {
		panic(err)
	}
	snapshotGauges, err := observabilitymetrics.NewSnapshotGauges(registry, time.Now)
	if err != nil {
		panic(err)
	}
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
		openRuntime: func(ctx context.Context, cfg config.Config, paths datadir.Paths, artifacts dockerruntime.ArtifactProvider) (managedRuntime, error) {
			return openProductionRuntimeWithMetrics(ctx, cfg, paths, artifacts, reliabilityMetrics)
		},
		startWorker: func(ctx context.Context, cfg config.Config, paths datadir.Paths, sandboxStore store.Store,
			runtime runtimeport.Runtime, queue *reconcile.WakeQueue) (workerHandle, error) {
			return startProductionWorkerWithMetricsAndGauges(ctx, cfg, paths, sandboxStore, runtime, queue, reliabilityMetrics, snapshotGauges)
		},
		startMaintenance: func(ctx context.Context, cfg config.Config, sandboxStore store.Store, runtime runtimeport.Runtime,
			queue *reconcile.WakeQueue, readiness *controlapi.Readiness) (workerHandle, error) {
			return startProductionMaintenanceWithMetricsAndGauges(ctx, cfg, sandboxStore, runtime, queue, readiness, reliabilityMetrics, snapshotGauges)
		},
		recover: func(ctx context.Context, cfg config.Config, paths datadir.Paths, sandboxStore store.Store,
			runtime runtimeport.Runtime, queue *reconcile.WakeQueue, readiness *controlapi.Readiness) error {
			return recoverProductionStateWithMetrics(ctx, cfg, paths, sandboxStore, runtime, queue, readiness, reliabilityMetrics)
		},
		startHTTP: func(cfg config.Config, build controlapi.BuildInfo, sandboxStore store.Store, runtime runtimeport.Runtime,
			queue *reconcile.WakeQueue, readiness *controlapi.Readiness) (httpHandle, error) {
			return startProductionHTTPWithAdmin(cfg, build, sandboxStore, runtime, queue, readiness, reliabilityMetrics, executionMetrics, registry, snapshotGauges)
		},
	}
}

const maxCandidateScanPages = 10_000

// startProductionMaintenance 启动周期 candidate scanner 与幂等记录 GC。
func startProductionMaintenance(ctx context.Context, cfg config.Config, sandboxStore store.Store, runtime runtimeport.Runtime, queue *reconcile.WakeQueue, readiness *controlapi.Readiness) (workerHandle, error) {
	return startProductionMaintenanceWithMetrics(ctx, cfg, sandboxStore, runtime, queue, readiness, nil)
}

func startProductionMaintenanceWithMetrics(ctx context.Context, cfg config.Config, sandboxStore store.Store, runtime runtimeport.Runtime, queue *reconcile.WakeQueue, readiness *controlapi.Readiness, metrics *observabilitymetrics.ReliabilityMetrics) (workerHandle, error) {
	return startProductionMaintenanceWithMetricsAndGauges(ctx, cfg, sandboxStore, runtime, queue, readiness, metrics, nil)
}

func startProductionMaintenanceWithMetricsAndGauges(ctx context.Context, cfg config.Config, sandboxStore store.Store, runtime runtimeport.Runtime, queue *reconcile.WakeQueue, readiness *controlapi.Readiness, metrics *observabilitymetrics.ReliabilityMetrics, gauges *observabilitymetrics.SnapshotGauges) (workerHandle, error) {
	var snapshotSource observabilitymetrics.SnapshotSource
	if gauges != nil {
		var ok bool
		snapshotSource, ok = sandboxStore.(observabilitymetrics.SnapshotSource)
		if !ok {
			return nil, errors.New("sandbox store does not provide metrics snapshots")
		}
	}
	ttlStore, ok := sandboxStore.(reconcile.TTLRecoveryStore)
	if !ok {
		return nil, errors.New("sandbox store does not provide TTL recovery")
	}
	var ttlExpiration *reconcile.TTLExpirationCoordinator
	ttlScheduler := reconcile.NewTTLScheduler(reconcile.SystemClock{}, func(ctx context.Context, entry reconcile.TTLHeapEntry) {
		if err := ttlExpiration.ExpireEntry(ctx, entry); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("TTL expiration failed", "reason", runtimeport.ClassifyError(err).Reason)
		}
	})
	// coordinator 和 scheduler 必须共享同一 index；重建 coordinator 后再恢复，
	// 且 Recover 返回前绝不启动 timer loop。
	ttlExpiration = reconcile.NewTTLExpirationCoordinator(ttlStore, ttlScheduler, reconcile.SystemClock{}, queue.Wake)
	if metrics != nil {
		ttlExpiration.SetMetrics(metrics)
	}
	ttlRecovery, err := reconcile.NewTTLRecovery(ttlStore, ttlScheduler, ttlExpiration, cfg.Reconcile.PageSize, maxCandidateScanPages)
	if err != nil {
		return nil, err
	}
	if err := ttlRecovery.Recover(ctx); err != nil {
		return nil, err
	}
	sweeper, err := reconcile.NewCandidateSweeper(
		sandboxStore, cfg.Reconcile.PageSize, maxCandidateScanPages,
		cfg.Reconcile.Timeout, cfg.Reconcile.RunningCheckInterval,
	)
	if err != nil {
		return nil, err
	}
	scanner, err := reconcile.NewCandidateScanner(sweeper, func(_ context.Context, id string) error {
		if testcrashpoint.Drop("scanner.wake") {
			return errors.New("test scanner wake dropped")
		}
		queue.Wake(id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	report := func(err error) {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("reliability maintenance failed", "reason", runtimeport.ClassifyError(err).Reason)
		}
	}
	loop, err := reconcile.NewScannerLoop(scanner, reconcile.SystemClock{}, reconcile.CryptoRandom{}, cfg.Reconcile.Interval, cfg.Reconcile.Jitter, report)
	if err != nil {
		return nil, err
	}
	gcStore, ok := sandboxStore.(reconcile.IdempotencyGCStore)
	if !ok {
		return nil, errors.New("sandbox store does not provide idempotency GC")
	}
	gc, err := reconcile.NewIdempotencyGC(gcStore, cfg.Idempotency.TerminalRetention, cfg.Idempotency.GCInterval, cfg.Reconcile.PageSize, report)
	if err != nil {
		return nil, err
	}
	storeProbe, ok := sandboxStore.(reconcile.DependencyProbe)
	if !ok {
		return nil, errors.New("sandbox store does not provide dependency probe")
	}
	dockerProbe, ok := runtime.(reconcile.DependencyProbe)
	if !ok {
		return nil, errors.New("sandbox runtime does not provide dependency probe")
	}
	availability, ok := runtime.(runtimeport.OperationAvailability)
	if !ok {
		return nil, errors.New("sandbox runtime does not provide operation availability gate")
	}
	probeTimeout := min(5*time.Second, cfg.Reconcile.DockerFreshness/2)
	health, err := reconcile.NewDependencyHealthMonitor(
		storeProbe, dockerProbe, readiness, availability, reconcile.SystemClock{},
		cfg.Reconcile.Interval, probeTimeout, cfg.Reconcile.DockerFreshness, report,
	)
	if err != nil {
		return nil, err
	}
	maintenanceCtx, cancel := context.WithCancel(ctx)
	handle := &runningMaintenance{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(handle.done)
		var wait sync.WaitGroup
		workers := 4
		if gauges != nil {
			workers++
		}
		wait.Add(workers)
		go func() { defer wait.Done(); ttlScheduler.Run(maintenanceCtx) }()
		go func() { defer wait.Done(); loop.Run(maintenanceCtx) }()
		go func() { defer wait.Done(); gc.Run(maintenanceCtx) }()
		go func() { defer wait.Done(); health.Run(maintenanceCtx) }()
		if gauges != nil {
			go func() {
				defer wait.Done()
				ticker := time.NewTicker(cfg.Reconcile.Interval)
				defer ticker.Stop()
				for {
					gauges.UpdateQueueDepth(queue.Len())
					_ = gauges.SampleStore(maintenanceCtx, snapshotSource, min(2*time.Second, cfg.Reconcile.Interval), 100000)
					select {
					case <-maintenanceCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		wait.Wait()
	}()
	return handle, nil
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
	return openProductionRuntimeWithMetrics(ctx, cfg, paths, artifacts, nil)
}

func openProductionRuntimeWithMetrics(
	ctx context.Context, cfg config.Config, paths datadir.Paths, artifacts dockerruntime.ArtifactProvider,
	metrics *observabilitymetrics.ReliabilityMetrics,
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
	imagePullLimiter, err := runtimeport.NewLimiter(cfg.Limits.MaxConcurrentImagePulls)
	if err != nil {
		return nil, errors.Join(err, stager.Close())
	}
	availability := runtimeport.NewAvailabilityGate(true)
	runtime, err := dockerruntime.New(
		ctx,
		cfg.Runtime.DockerHost,
		dockerruntime.RuntimeOptions{
			DataDirectory:    paths.DataDirectory,
			Artifacts:        artifacts,
			CreateTimeout:    defaultRuntimeCreateTimeout,
			Egress:           egress,
			Bootstrap:        stager,
			ImagePullLimiter: imagePullLimiter,
			Availability:     availability,
			Metrics:          metrics,
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
	return startProductionWorkerWithMetrics(ctx, cfg, paths, sandboxStore, runtime, queue, nil)
}

func startProductionWorkerWithMetrics(
	ctx context.Context, cfg config.Config, paths datadir.Paths, sandboxStore store.Store,
	runtime runtimeport.Runtime, queue *reconcile.WakeQueue, metrics *observabilitymetrics.ReliabilityMetrics,
) (workerHandle, error) {
	return startProductionWorkerWithMetricsAndGauges(ctx, cfg, paths, sandboxStore, runtime, queue, metrics, nil)
}

func startProductionWorkerWithMetricsAndGauges(
	ctx context.Context, cfg config.Config, paths datadir.Paths, sandboxStore store.Store,
	runtime runtimeport.Runtime, queue *reconcile.WakeQueue, metrics *observabilitymetrics.ReliabilityMetrics,
	gauges *observabilitymetrics.SnapshotGauges,
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
	var probe reconcile.RunnerProbe = factory
	if metrics != nil {
		probe, err = reconcile.NewMetricsRunnerProbe(factory, metrics)
		if err != nil {
			factory.Close()
			return nil, err
		}
	}
	reconciler, err := reconcile.NewWithShutdownRetryAndLimits(
		sandboxStore, runtime, probe, factory,
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
	safeLogger, err := logging.New(slog.Default())
	if err != nil {
		factory.Close()
		return nil, err
	}
	operationLogger, err := reconcile.NewOperationLogger(safeLogger, reconcile.SystemClock{})
	if err != nil {
		factory.Close()
		return nil, err
	}
	reconciler.SetOperationLogger(operationLogger)
	if metrics != nil {
		reconciler.SetMetrics(metrics)
	}
	leaseWriter, err := runtimeport.NewLeaseManifestWriter(paths.RunRoot)
	if err != nil {
		factory.Close()
		return nil, err
	}
	reconciler.SetLeaseProjector(leaseWriter)
	reconcileTask := reconciler.Reconcile
	if gauges != nil {
		reconcileTask = func(ctx context.Context, sandboxID string) error {
			gauges.UpdateQueueDepth(queue.Len())
			gauges.WorkerStarted()
			defer gauges.WorkerFinished()
			return reconciler.Reconcile(ctx, sandboxID)
		}
	}
	worker, err := reconcile.NewWorkerPool(
		queue,
		cfg.Reconcile.MaxConcurrent,
		cfg.Reconcile.Timeout,
		reconcileTask,
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
	cfg config.Config,
	_ datadir.Paths,
	sandboxStore store.Store,
	runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue,
	readiness *controlapi.Readiness,
) error {
	return recoverProductionStateWithMetrics(ctx, cfg, datadir.Paths{}, sandboxStore, runtime, queue, readiness, nil)
}

func recoverProductionStateWithMetrics(
	ctx context.Context, cfg config.Config, _ datadir.Paths, sandboxStore store.Store, runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue, readiness *controlapi.Readiness, metrics *observabilitymetrics.ReliabilityMetrics,
) error {
	inventory, ok := runtime.(runtimeport.RecoveryInventory)
	if !ok {
		return errors.New("sandbox runtime does not provide complete recovery inventory")
	}
	network, ok := runtime.(runtimeport.EgressRecoveryBootstrap)
	if !ok {
		return errors.New("sandbox runtime does not provide egress recovery bootstrap")
	}
	expectations, ok := runtime.(runtimeport.RecoveryExpectationProvider)
	if !ok {
		return errors.New("sandbox runtime does not provide recovery expectations")
	}
	anomalies, ok := sandboxStore.(store.RuntimeAnomalyRepository)
	if !ok {
		return errors.New("sandbox store does not provide runtime anomaly repository")
	}
	safeLogger, err := logging.New(slog.Default())
	if err != nil {
		return err
	}
	operationLogger, err := reconcile.NewOperationLogger(safeLogger, reconcile.SystemClock{})
	if err != nil {
		return err
	}
	stages := reconcile.StartupRecoveryStages{}
	stages.Recover = func(ctx context.Context, actual reconcile.ActualResourceInventory, scanStartedAt time.Time) error {
		expected, err := expectations.RecoveryExpectation(ctx)
		if err != nil {
			return err
		}
		executor, err := reconcile.NewInventoryRecoveryExecutor(
			sandboxStore, anomalies, reconcile.SystemClock{}, queue.Wake,
			cfg.Recovery.ImportTrustedOrphans, reconcile.DriftExpectation{EgressPolicyHash: expected.EgressPolicyHash},
		)
		if err != nil {
			return err
		}
		executor.SetOperationLogger(operationLogger)
		if metrics != nil {
			executor.SetMetrics(metrics)
		}
		return executor.Recover(ctx, actual, scanStartedAt)
	}
	stages.RecoverTTL = func(ctx context.Context) error {
		ttlStore, ok := sandboxStore.(reconcile.TTLRecoveryStore)
		if !ok {
			return errors.New("sandbox store does not provide TTL recovery")
		}
		var expiration *reconcile.TTLExpirationCoordinator
		scheduler := reconcile.NewTTLScheduler(reconcile.SystemClock{}, func(ctx context.Context, entry reconcile.TTLHeapEntry) {
			_ = expiration.ExpireEntry(ctx, entry)
		})
		expiration = reconcile.NewTTLExpirationCoordinator(ttlStore, scheduler, reconcile.SystemClock{}, queue.Wake)
		if metrics != nil {
			expiration.SetMetrics(metrics)
		}
		recovery, err := reconcile.NewTTLRecovery(ttlStore, scheduler, expiration, cfg.Reconcile.PageSize, maxCandidateScanPages)
		if err != nil {
			return err
		}
		return recovery.Recover(ctx)
	}
	stages.QueueDue = func(ctx context.Context) error {
		sweeper, err := reconcile.NewCandidateSweeper(sandboxStore, cfg.Reconcile.PageSize, maxCandidateScanPages,
			cfg.Reconcile.Timeout, cfg.Reconcile.RunningCheckInterval)
		if err != nil {
			return err
		}
		scanner, err := reconcile.NewCandidateScanner(sweeper, func(_ context.Context, id string) error {
			queue.Wake(id)
			return nil
		})
		if err != nil {
			return err
		}
		_, err = scanner.ScanOnce(ctx, time.Now().UTC())
		return err
	}
	coordinator, err := reconcile.NewStartupRecoveryCoordinator(network, inventory, stages, cfg.Reconcile.Timeout)
	if err != nil {
		return err
	}
	readiness.SetRecovery(false)
	return coordinator.Run(ctx)
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
	return startProductionHTTPWithMetrics(cfg, build, sandboxStore, runtime, queue, readiness, nil, nil)
}

func startProductionHTTPWithMetrics(
	cfg config.Config, build controlapi.BuildInfo, sandboxStore store.Store, runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue, readiness *controlapi.Readiness, reliabilityMetrics *observabilitymetrics.ReliabilityMetrics,
	executionMetrics *observabilitymetrics.ExecutionCounters,
) (httpHandle, error) {
	return startProductionHTTPWithAdmin(cfg, build, sandboxStore, runtime, queue, readiness, reliabilityMetrics, executionMetrics, nil, nil)
}

func startProductionHTTPWithAdmin(cfg config.Config, build controlapi.BuildInfo, sandboxStore store.Store, runtime runtimeport.Runtime,
	queue *reconcile.WakeQueue, readiness *controlapi.Readiness, reliabilityMetrics *observabilitymetrics.ReliabilityMetrics,
	executionMetrics *observabilitymetrics.ExecutionCounters, registry *observabilitymetrics.Registry, gauges *observabilitymetrics.SnapshotGauges) (httpHandle, error) {
	lifecycleService := application.NewSandboxServiceWithCreatePolicy(
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
	safeLogger, err := logging.New(slog.Default())
	if err != nil {
		return nil, err
	}
	lifecycle, err := application.NewLoggingSandboxService(lifecycleService, safeLogger, application.SystemClock{})
	if err != nil {
		return nil, err
	}
	var lifecycleAPI controlapi.LifecycleService = lifecycle
	if reliabilityMetrics != nil {
		lifecycleAPI, err = application.NewMetricsLifecycleService(lifecycle, reliabilityMetrics)
		if err != nil {
			return nil, err
		}
	}
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
	var executionAPI controlapi.ExecutionService = execution
	if executionMetrics != nil {
		executionAPI, err = application.NewMetricsExecutionService(execution, executionMetrics)
		if err != nil {
			runnerFactory.Close()
			return nil, err
		}
	}
	filesService, err := application.NewFilesService(sandboxStore, applicationFilesFactory{factory: runnerFactory})
	if err != nil {
		runnerFactory.Close()
		return nil, err
	}
	deps := controlapi.RouterDependencies{Lifecycle: lifecycleAPI, Execution: executionAPI, SSEWriteTimeout: cfg.Runner.SSEWriteTimeout, Readiness: readiness, Files: filesService}
	if cfg.Admin.Enabled {
		token, loadErr := adminauth.LoadToken(cfg.Admin.TokenFile)
		if loadErr != nil {
			runnerFactory.Close()
			return nil, loadErr
		}
		authenticate := observabilitymetrics.AuthMiddleware(token.Middleware)
		if registry == nil {
			runnerFactory.Close()
			return nil, errors.New("metrics registry is not configured")
		}
		deps.Metrics = observabilitymetrics.NewHandler(registry.Gatherer(), authenticate, min(5*time.Second, cfg.Server.ShutdownTimeout), 2)
		diagnosticsStore, ok := sandboxStore.(application.DiagnosticsStore)
		if !ok {
			runnerFactory.Close()
			return nil, errors.New("sandbox store does not provide diagnostics snapshots")
		}
		diagnostics, diagnosticsErr := application.NewDiagnosticsService(diagnosticsStore, runtimeDiagnosticsAdapter{runtime: runtime}, schedulerDiagnosticsAdapter{queue: queue, gauges: gauges}, min(2*time.Second, cfg.Server.ShutdownTimeout), time.Now, runnerDiagnosticsAdapter{store: diagnosticsStore, factory: runnerFactory})
		if diagnosticsErr != nil {
			runnerFactory.Close()
			return nil, diagnosticsErr
		}
		deps.Diagnostics = token.Middleware(controlapi.NewDiagnosticsHandler(diagnostics))
	}
	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           controlapi.NewRouter(build, deps),
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

type runtimeDiagnosticsAdapter struct{ runtime runtimeport.Runtime }

func (a runtimeDiagnosticsAdapter) Diagnostics(ctx context.Context) (application.RuntimeDiagnostics, error) {
	inventory, ok := a.runtime.(runtimeport.RecoveryInventory)
	if !ok {
		return application.RuntimeDiagnostics{}, errors.New("runtime inventory unavailable")
	}
	containers, err := inventory.InventoryManagedContainers(ctx)
	if err != nil {
		return application.RuntimeDiagnostics{}, err
	}
	seen, outbound := map[string]struct{}{}, map[string]struct{}{}
	for _, item := range containers {
		seen[item.SandboxID] = struct{}{}
		if item.Role == runtimeport.ContainerRoleEgress {
			outbound[item.SandboxID] = struct{}{}
		}
	}
	return application.RuntimeDiagnostics{ManagedSandboxes: len(seen), OutboundSandboxes: len(outbound)}, nil
}

type runnerDiagnosticsAdapter struct {
	store   application.DiagnosticsStore
	factory *runnerclient.Factory
}

func (a runnerDiagnosticsAdapter) Diagnostics(ctx context.Context) (application.RunnerDiagnostics, error) {
	records, err := a.store.ListAll(ctx)
	if err != nil {
		return application.RunnerDiagnostics{}, err
	}
	result := application.RunnerDiagnostics{}
	for _, record := range records {
		if record.ObservedState != domain.StateRunning {
			continue
		}
		if err := a.factory.Probe(ctx, record.ID, runnerbootstrap.CurrentProtocolVersion); err != nil {
			result.Unavailable++
			continue
		}
		result.Ready++
	}
	return result, nil
}

type schedulerDiagnosticsAdapter struct {
	queue  *reconcile.WakeQueue
	gauges *observabilitymetrics.SnapshotGauges
}

func (a schedulerDiagnosticsAdapter) Diagnostics() application.SchedulerDiagnostics {
	if a.gauges != nil {
		queueDepth, activeWorkers := a.gauges.SchedulerSnapshot()
		return application.SchedulerDiagnostics{QueueDepth: queueDepth, ActiveWorkers: activeWorkers}
	}
	return application.SchedulerDiagnostics{QueueDepth: a.queue.Len()}
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

// runningMaintenance 管理 scanner 与 GC 的共同 lifetime。
type runningMaintenance struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Close 停止产生新 Wake/GC 事务并等待两个循环退出。
func (m *runningMaintenance) Close(ctx context.Context) error {
	m.once.Do(m.cancel)
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
