// Package main 提供 sandbox-init 容器 PID 1 可执行程序。
//
// 本模块设计上负责启动 runnerd、转发终止信号并回收孤儿进程；当前初始化骨架
// 已实现启动和信号转发，通用孤儿回收仍待实现。它不承载 HTTP API、命令执行
// 协议或 sandbox 生命周期业务。
package main

import (
	"log/slog"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		slog.Error("usage: sandbox-init -- command [args...]")
		os.Exit(2)
	}
	args := os.Args[1:]
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		slog.Error("sandbox-init requires a child command")
		os.Exit(2)
	}
	os.Exit(run(args))
}
