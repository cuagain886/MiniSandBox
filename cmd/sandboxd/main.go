// Package main 提供 sandboxd 宿主机控制面可执行程序。
//
// 本模块设计上负责装配 HTTP API、生命周期服务、持久化和 runtime adapter；
// 当前初始化骨架只装配健康检查与占位路由。它不能在宿主机直接执行用户命令。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	controlapi "minisandbox/internal/api"
)

var (
	version = "dev"
	commit  = ""
)

func main() {
	listenAddress := flag.String(
		"listen",
		"127.0.0.1:8080",
		"HTTP listen address",
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	server := &http.Server{
		Addr: *listenAddress,
		Handler: controlapi.NewRouter(controlapi.BuildInfo{
			Version: version,
			Commit:  commit,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("sandboxd shutdown failed", "error", err)
		}
	}()

	slog.Info("sandboxd listening", "address", *listenAddress, "version", version)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("sandboxd stopped", "error", err)
		os.Exit(1)
	}
}
