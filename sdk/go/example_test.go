package sdk_test

import (
	"context"
	"fmt"
	"time"

	"minisandbox/sdk/go"
)

// Example_quickstart 是只依赖高层 SDK 的最小可用流程：创建 sandbox、等待
// 就绪、执行一次命令并清理。用户可以直接复制本示例完成第一次执行。
func Example_quickstart() {
	client := sdk.NewClient("http://127.0.0.1:8080", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{
		Image: "debian:bookworm-slim",
	})
	if err != nil {
		fmt.Println("create:", err)
		return
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 30*time.Second,
		)
		defer cleanupCancel()
		_, _ = sandbox.DeleteAndWait(cleanupCtx)
	}()

	if _, err := sandbox.WaitRunning(ctx); err != nil {
		fmt.Println("wait running:", err)
		return
	}

	result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv:    []string{"/bin/sh", "-c", "echo hello"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		fmt.Println("run:", err)
		return
	}
	fmt.Printf("exit=%d stdout=%s\n", result.ExitCode, result.Stdout)
}
