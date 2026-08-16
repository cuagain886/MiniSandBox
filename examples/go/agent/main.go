// Example_agent 展示 Go SDK 的完整 Agent 工作流：
// 创建 → 等待就绪 → 上传源码 → 执行 → 下载产物 → 删除。
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"minisandbox/sdk/go"
)

func main() {
	client := sdk.NewClient("http://127.0.0.1:8080", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sandbox, err := client.Create(ctx, sdk.CreateSandboxRequest{
		Image: "debian:bookworm-slim",
	})
	if err != nil {
		abort("create", err)
	}
	defer sandbox.Delete(context.Background())

	_, capabilities, err := sandbox.WaitReady(ctx)
	if err != nil {
		abort("wait ready", err)
	}
	fmt.Println("capabilities:", capabilities)

	source := []byte("#!/bin/sh\necho agent-build-ok > artifact.txt\n")
	if err := sandbox.Files().Upload(ctx, "src/build.sh", bytes.NewReader(source),
		sdk.WithCreateParents()); err != nil {
		abort("upload", err)
	}

	result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv: []string{"/bin/sh", "/workspace/src/build.sh"}, Timeout: 30 * time.Second,
	})
	if err != nil {
		abort("run", err)
	}
	fmt.Printf("run exit=%d stdout=%q\n", result.ExitCode, result.Stdout)

	artifact, err := sandbox.Files().Download(ctx, "artifact.txt")
	if err != nil {
		abort("download", err)
	}
	content, _ := io.ReadAll(artifact)
	_ = artifact.Close()
	fmt.Printf("artifact=%q\n", content)

	if _, err := sandbox.DeleteAndWait(ctx); err != nil {
		abort("delete", err)
	}
	fmt.Println("agent workflow done")
}

func abort(step string, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(os.Stderr, step, "timeout:", err)
	} else {
		fmt.Fprintln(os.Stderr, step, "failed:", err)
	}
	os.Exit(1)
}
