// egressd 是 egress sidecar 的 PID 1 bootstrap 与 namespace anchor 入口。
package main

import (
	"context"
	"fmt"
	"os"

	"minisandbox/internal/egressnft"
)

func main() {
	policy, err := egressnft.ReadBootstrap(os.Stdin)
	if err == nil {
		err = egressnft.Install(context.Background(), egressnft.OSExecutor{}, policy)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "egress bootstrap failed")
		os.Exit(1)
	}
}
