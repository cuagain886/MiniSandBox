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
	if err := runner.Serve(ctx, listener, runner.NewServer(version, token)); err != nil {
		slog.Error("runnerd stopped", "error", err)
		os.Exit(1)
	}
}
