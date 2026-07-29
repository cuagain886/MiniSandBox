// Package main 提供 sandboxd 宿主机控制面可执行程序。
//
// 本模块设计上负责装配 HTTP API、生命周期服务、持久化和 runtime adapter；
// 当前初始化骨架只装配健康检查与占位路由。它不能在宿主机直接执行用户命令。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	controlapi "minisandbox/internal/api"
	"minisandbox/internal/bootstrap"
)

var (
	version = "dev"
	commit  = ""
)

func main() {
	configPath := flag.String(
		"config",
		"configs/sandboxd.example.yaml",
		"sandboxd YAML configuration path",
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	err := bootstrap.Run(ctx, bootstrap.Options{
		ConfigPath: *configPath,
		Build: controlapi.BuildInfo{
			Version: version,
			Commit:  commit,
		},
	})
	if err != nil {
		slog.Error("sandboxd stopped", "error", err)
		os.Exit(1)
	}
}
