// egressd 是 egress sidecar 的 PID 1 bootstrap 与 namespace anchor 入口。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egressnft"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "bootstrap" {
		fmt.Fprintln(os.Stderr, "usage: egressd bootstrap")
		os.Exit(2)
	}
	bootstrap, err := egressnft.ReadBootstrap(os.Stdin)
	if err == nil {
		err = egressnft.Install(context.Background(), egressnft.OSExecutor{}, bootstrap.Policy)
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = egressanchor.Activate(ctx, bootstrap, egressanchor.Options{
			Platform: egressanchor.LinuxPlatform{}, BootstrapInput: os.Stdin,
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "egress bootstrap failed")
		os.Exit(1)
	}
}
