// Package bootstrap 负责按安全顺序装配和关闭 sandboxd 的生产依赖。
//
// 本模块连接配置、目录、Store、Docker runtime、reconciler 和 HTTP server；
// 业务规则仍位于 application/reconcile，main 只负责 flag、信号和退出码。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/config"
	"minisandbox/internal/datadir"
	"minisandbox/internal/reconcile"
	runtimeport "minisandbox/internal/runtime"
	dockerruntime "minisandbox/internal/runtime/docker"
	"minisandbox/internal/store"
)

// Options 保存启动 sandboxd 所需的命令行输入和构建信息。
type Options struct {
	// ConfigPath 是显式 YAML 配置文件路径。
	ConfigPath string
	// Build 是健康检查返回的二进制版本信息。
	Build controlapi.BuildInfo
}

// managedStore 组合生命周期 Store 与可关闭资源。
type managedStore interface {
	store.Store
	io.Closer
}

// managedRuntime 组合 runtime 端口与 Docker client 关闭能力。
type managedRuntime interface {
	runtimeport.Runtime
	io.Closer
}

// workerHandle 表示已经启动且可等待退出的单 worker。
type workerHandle interface {
	Close(context.Context) error
}

// httpHandle 表示已经完成端口绑定的 HTTP server。
type httpHandle interface {
	Done() <-chan error
	Close(context.Context) error
}

// factories 封装启动阶段的外部副作用，供顺序和失败清理测试替换。
type factories struct {
	loadConfig  func(string) (config.Config, error)
	directories func(config.Config) (datadir.Paths, error)
	openStore   func(context.Context, datadir.Paths) (managedStore, error)
	artifacts   func() (dockerruntime.ArtifactProvider, error)
	openRuntime func(
		context.Context,
		config.Config,
		datadir.Paths,
		dockerruntime.ArtifactProvider,
	) (managedRuntime, error)
	startWorker func(
		context.Context,
		config.Config,
		datadir.Paths,
		store.Store,
		runtimeport.Runtime,
		*reconcile.WakeQueue,
	) (workerHandle, error)
	recover func(
		context.Context,
		store.Store,
		runtimeport.Runtime,
		*reconcile.WakeQueue,
		*controlapi.Readiness,
	) error
	startHTTP func(
		config.Config,
		controlapi.BuildInfo,
		store.Store,
		*reconcile.WakeQueue,
		*controlapi.Readiness,
	) (httpHandle, error)
}

// Run 装配 sandboxd，阻塞到 shutdown 或 HTTP server 异常退出。
//
// 所有启动失败和正常关闭路径都按依赖逆序释放资源；返回前不会遗留 worker
// 或 HTTP goroutine。
func Run(ctx context.Context, options Options) error {
	return run(ctx, options, productionFactories())
}

// run 使用可替换 factories 执行固定启动状态机。
func run(ctx context.Context, options Options, factory factories) error {
	cfg, err := factory.loadConfig(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("load sandboxd configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate sandboxd configuration: %w", err)
	}
	paths, err := factory.directories(cfg)
	if err != nil {
		return fmt.Errorf("prepare sandboxd directories: %w", err)
	}
	sandboxStore, err := factory.openStore(ctx, paths)
	if err != nil {
		return fmt.Errorf("open sandbox store: %w", err)
	}

	artifactProvider, err := factory.artifacts()
	if err != nil {
		return errors.Join(
			fmt.Errorf("load sandbox artifacts: %w", err),
			closeResource("sandbox store", sandboxStore),
		)
	}
	runtime, err := factory.openRuntime(
		ctx,
		cfg,
		paths,
		artifactProvider,
	)
	if err != nil {
		return errors.Join(
			fmt.Errorf("open sandbox runtime: %w", err),
			closeResource("sandbox store", sandboxStore),
		)
	}

	queue := reconcile.NewWakeQueue()
	worker, err := factory.startWorker(
		ctx,
		cfg,
		paths,
		sandboxStore,
		runtime,
		queue,
	)
	if err != nil {
		return errors.Join(
			fmt.Errorf("start reconcile worker: %w", err),
			closeResource("sandbox runtime", runtime),
			closeResource("sandbox store", sandboxStore),
		)
	}
	readiness := &controlapi.Readiness{}
	if err := factory.recover(
		ctx,
		sandboxStore,
		runtime,
		queue,
		readiness,
	); err != nil {
		return errors.Join(
			fmt.Errorf("recover sandbox state: %w", err),
			closeWithTimeout(cfg, "reconcile worker", worker),
			closeResource("sandbox runtime", runtime),
			closeResource("sandbox store", sandboxStore),
		)
	}
	server, err := factory.startHTTP(
		cfg,
		options.Build,
		sandboxStore,
		queue,
		readiness,
	)
	if err != nil {
		return errors.Join(
			fmt.Errorf("start sandbox HTTP server: %w", err),
			closeWithTimeout(cfg, "reconcile worker", worker),
			closeResource("sandbox runtime", runtime),
			closeResource("sandbox store", sandboxStore),
		)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case serveErr := <-server.Done():
		if serveErr != nil {
			runErr = fmt.Errorf("serve sandbox HTTP API: %w", serveErr)
		}
	}
	return errors.Join(
		runErr,
		closeWithTimeout(cfg, "sandbox HTTP server", server),
		closeWithTimeout(cfg, "reconcile worker", worker),
		closeResource("sandbox runtime", runtime),
		closeResource("sandbox store", sandboxStore),
	)
}

// closeWithTimeout 给可能等待 goroutine 的关闭步骤设置统一 shutdown 边界。
func closeWithTimeout(
	cfg config.Config,
	name string,
	resource interface {
		Close(context.Context) error
	},
) error {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		cfg.Server.ShutdownTimeout,
	)
	defer cancel()
	if err := resource.Close(ctx); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

// closeResource 关闭无 context 资源并添加稳定阶段说明。
func closeResource(name string, resource io.Closer) error {
	if err := resource.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

const defaultRuntimeCreateTimeout = 10 * time.Minute
