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
