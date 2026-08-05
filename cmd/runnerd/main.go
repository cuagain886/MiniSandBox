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
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"minisandbox/internal/runner"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		slog.Error("usage: runnerd serve [-socket path]")
		os.Exit(2)
	}

	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	socketPath := flags.String(
		"socket",
		"/run/minisandbox/runner.sock",
		"Unix socket path",
	)
	_ = flags.Parse(os.Args[2:])
	token := os.Getenv("MINISANDBOX_RUNNER_TOKEN")

	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o700); err != nil {
		slog.Error("create socket directory", "error", err)
		os.Exit(1)
	}
	// socket 路径由 sandboxd 为当前 sandbox 独立挂载。这里只清理同一路径上的
	// 失效 socket，不扫描也不影响其他 sandbox 的运行目录。
	if err := os.Remove(*socketPath); err != nil && !os.IsNotExist(err) {
		slog.Error("remove stale socket", "error", err)
		os.Exit(1)
	}

	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		slog.Error("listen on runner socket", "error", err)
		os.Exit(1)
	}
	defer listener.Close()
	if err := os.Chmod(*socketPath, 0o600); err != nil {
		slog.Error("secure runner socket", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	slog.Info("runnerd listening", "socket", *socketPath, "version", version)
	handler, err := runner.NewServer(version, token)
	if err != nil {
		slog.Error("configure runner server", "error", err)
		os.Exit(1)
	}
	if err := runner.Serve(ctx, listener, handler); err != nil {
		slog.Error("runnerd stopped", "error", err)
		os.Exit(1)
	}
}
