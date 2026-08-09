// Package main 提供 runnerd 容器内执行数据面可执行程序。
//
// 本模块只服务当前 sandbox 的 Unix Socket；当前初始化骨架已实现 HTTP 服务启动、
// 鉴权和退出信号接收，命令执行仍待实现。它不能访问 Docker socket，也不能管理
// 其他 sandbox。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minisandbox/internal/runner"
	"minisandbox/internal/runnerbootstrap"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		slog.Error("usage: runnerd serve [-socket path]")
		os.Exit(2)
	}

	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = flags.Parse(os.Args[2:])
	bootstrap, token, err := runner.WaitLoadBootstrapMaterial(runnerbootstrap.RuntimeDirectory, 5*time.Second)
	if err != nil {
		slog.Error("load runner bootstrap", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	slog.Info("runnerd starting", "sandbox_id", bootstrap.SandboxID, "version", version)
	if err := runner.ServeConfigured(ctx, version, bootstrap, &token); err != nil {
		slog.Error("runnerd stopped", "error", err)
		os.Exit(1)
	}
}
