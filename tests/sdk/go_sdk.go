// Package main 提供只依赖公开 Go SDK 高层接口的 MiniSandbox 端到端验收程序。
// 它验证调用方可见的核心闭环，不替代网络隔离、进程残留和崩溃恢复安全测试。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"minisandbox/sdk/go"
)

const (
	defaultBaseURL = "http://127.0.0.1:8080"
	defaultImage   = "debian:bookworm-slim"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nSDK 验收失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n10/10 PASS：Go SDK 核心功能验收通过")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	baseURL := environmentOrDefault("MINISANDBOX_URL", defaultBaseURL)
	image := environmentOrDefault("MINISANDBOX_IMAGE", defaultImage)
	client := sdk.NewClient(baseURL, &http.Client{Timeout: 15 * time.Second})

	fmt.Printf("MiniSandbox SDK 验收：server=%s image=%s\n", baseURL, image)

	// S10 是环境预检，先于所有资源操作执行。
	if err := client.Health(ctx); err != nil {
		return fmt.Errorf("S10 health: %w", err)
	}
	readiness, err := client.Readiness(ctx)
	if err != nil {
		return fmt.Errorf("S10 readiness: %w", err)
	}
	if !readiness.Ready {
		components := make([]string, 0, len(readiness.Components))
		for _, component := range readiness.Components {
			components = append(components, fmt.Sprintf("%s=%v", component.Name, component.Ready))
		}
		return fmt.Errorf("S10 服务未就绪: %s", strings.Join(components, " "))
	}
	pass("S10", "Health 存活且 Readiness 组件全部就绪", "")

	ttl := 10 * time.Minute
	createRequest := sdk.CreateSandboxRequest{Image: image, TTL: &ttl}
	idempotencyKey := fmt.Sprintf("sdk-acceptance-%d", time.Now().UTC().UnixNano())

	sandbox, err := client.Create(ctx, createRequest, sdk.WithIdempotencyKey(idempotencyKey))
	if err != nil {
		return fmt.Errorf("S01 创建 sandbox: %w", err)
	}
	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = sandbox.Delete(cleanupCtx)
		}
	}()

	runningInfo, err := sandbox.WaitRunning(ctx)
	if err != nil {
		return fmt.Errorf("S01 等待 Running: %w", err)
	}
	if runningInfo.ExpiresAt.IsZero() || !runningInfo.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("S01 sandbox expiry 无效: %s", runningInfo.ExpiresAt)
	}
	pass("S01", "创建 sandbox 并进入 Running", sandbox.ID())

	replayed, err := client.Create(ctx, createRequest, sdk.WithIdempotencyKey(idempotencyKey))
	if err != nil {
		return fmt.Errorf("S02 幂等重放: %w", err)
	}
	if replayed.ID() != sandbox.ID() {
		return fmt.Errorf("S02 幂等重放创建了不同 sandbox: %s != %s", replayed.ID(), sandbox.ID())
	}
	conflictingTTL := 11 * time.Minute
	_, err = client.Create(ctx, sdk.CreateSandboxRequest{
		Image: image,
		TTL:   &conflictingTTL,
	}, sdk.WithIdempotencyKey(idempotencyKey))
	if err := expectResponseError(err, http.StatusConflict, "IDEMPOTENCY_CONFLICT"); err != nil {
		return fmt.Errorf("S02 幂等冲突: %w", err)
	}
	pass("S02", "幂等重放返回同一资源，不同请求返回 409", sandbox.ID())

	execution, err := sandbox.StartExecution(ctx, sdk.ExecuteRequest{
		Argv: []string{
			"/bin/sh", "-c",
			"printf 'sdk-stdout'; printf 'sdk-stderr' >&2",
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S03 创建 execution: %w", err)
	}
	terminalInfo, err := execution.Wait(ctx)
	if err != nil {
		return fmt.Errorf("S03 等待 execution 终态: %w", err)
	}
	if terminalInfo.State != sdk.ExecutionStateExited ||
		terminalInfo.TerminalEvent == nil ||
		terminalInfo.TerminalEvent.Type != sdk.EventExited ||
		terminalInfo.TerminalEvent.ExitCode != 0 {
		return fmt.Errorf("S03 非预期终态: %+v", terminalInfo)
	}
	var stdout, stderr strings.Builder
	logs := execution.Logs(ctx, 0)
	for logs.Next() {
		event := logs.Event()
		switch event.Type {
		case sdk.EventStdout:
			stdout.Write(event.Data)
		case sdk.EventStderr:
			stderr.Write(event.Data)
		}
	}
	if err := logs.Err(); err != nil {
		return fmt.Errorf("S03 读取日志: %w", err)
	}
	if stdout.String() != "sdk-stdout" || stderr.String() != "sdk-stderr" {
		return fmt.Errorf("S03 输出不匹配: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	pass("S03", "命令退出码、stdout/stderr 日志自动解码正确", execution.ID())

	cancellable, err := sandbox.StartExecution(ctx, sdk.ExecuteRequest{
		Argv:    []string{"/bin/sh", "-c", "sleep 30 & wait"},
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S04 创建待取消 execution: %w", err)
	}
	cancelledInfo, err := cancellable.CancelAndWait(ctx)
	if err != nil {
		return fmt.Errorf("S04 取消并等待终态: %w", err)
	}
	if cancelledInfo.State != sdk.ExecutionStateCancelled ||
		cancelledInfo.TerminalEvent == nil ||
		cancelledInfo.TerminalEvent.Type != sdk.EventCancelled {
		return fmt.Errorf("S04 非预期取消终态: %+v", cancelledInfo)
	}
	if _, err := cancellable.CancelAndWait(ctx); err != nil {
		return fmt.Errorf("S04 重复取消: %w", err)
	}
	pass("S04", "长任务取消并保持稳定 Cancelled 终态", cancellable.ID())

	previousExpiry := runningInfo.ExpiresAt
	requestedExpiry := previousExpiry.Add(5 * time.Minute)
	renewedInfo, err := sandbox.Renew(ctx, requestedExpiry)
	if err != nil {
		return fmt.Errorf("S05 续期: %w", err)
	}
	if !renewedInfo.ExpiresAt.Equal(requestedExpiry) {
		return fmt.Errorf("S05 续期结果不匹配: got=%s want=%s", renewedInfo.ExpiresAt, requestedExpiry)
	}
	replayedRenew, err := sandbox.Renew(ctx, requestedExpiry)
	if err != nil || !replayedRenew.ExpiresAt.Equal(requestedExpiry) {
		return fmt.Errorf("S05 续期重放: expiry=%s err=%w", replayedRenew.ExpiresAt, err)
	}
	_, err = sandbox.Renew(ctx, previousExpiry)
	if err := expectResponseError(err, http.StatusConflict, "LEASE_CONFLICT"); err != nil {
		return fmt.Errorf("S05 拒绝缩短租约: %w", err)
	}
	pass("S05", "续期、幂等重放和拒绝缩短租约", renewedInfo.ExpiresAt.Format(time.RFC3339))

	result, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv: []string{
			"/bin/sh", "-c",
			"printf 'run-stdout'; printf 'run-stderr' >&2",
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S08 Run 正常执行: %w", err)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "run-stdout" ||
		string(result.Stderr) != "run-stderr" {
		return fmt.Errorf("S08 Run 结果不匹配: %+v", result)
	}
	nonzero, err := sandbox.Run(ctx, sdk.ExecuteRequest{
		Argv:    []string{"/bin/sh", "-c", "exit 7"},
		Timeout: 10 * time.Second,
	})
	var exitError *sdk.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode != 7 {
		return fmt.Errorf("S08 Run 非零退出: got %v, want ExitError(7)", err)
	}
	if nonzero.ExitCode != 7 || nonzero.State != sdk.ExecutionStateExited {
		return fmt.Errorf("S08 非零退出结果不完整: %+v", nonzero)
	}
	pass("S08", "Run 一次调用收集输出并区分退出码", result.ExecutionID)

	stream, err := sandbox.ExecuteStream(ctx, sdk.ExecuteRequest{
		Argv: []string{
			"/bin/sh", "-c",
			"printf 'sse-stdout'; printf 'sse-stderr' >&2",
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S09 前台 SSE 执行: %w", err)
	}
	var streamStdout, streamStderr strings.Builder
	terminated := false
	for stream.Next() {
		event := stream.Event()
		switch event.Type {
		case sdk.EventStdout:
			streamStdout.Write(event.Data)
		case sdk.EventStderr:
			streamStderr.Write(event.Data)
		case sdk.EventExited:
			terminated = event.ExitCode == 0
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("S09 前台 SSE 流: %w", err)
	}
	if !terminated || streamStdout.String() != "sse-stdout" || streamStderr.String() != "sse-stderr" {
		return fmt.Errorf(
			"S09 SSE 输出不匹配: terminated=%v stdout=%q stderr=%q",
			terminated,
			streamStdout.String(),
			streamStderr.String(),
		)
	}
	pass("S09", "前台 SSE 边执行边解码输出", "")

	_, err = client.Sandbox("00000000-0000-4000-8000-000000000000").Info(ctx)
	if err := expectResponseError(err, http.StatusNotFound, ""); err != nil {
		return fmt.Errorf("S06 404 错误模型: %w", err)
	}
	cancelledContext, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	_, err = client.Sandbox(sandbox.ID()).Info(cancelledContext)
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("S06 context 取消: got %v, want context.Canceled", err)
	}
	invalidTTL := 30 * time.Second
	_, err = client.Create(ctx, sdk.CreateSandboxRequest{
		Image: image,
		TTL:   &invalidTTL,
	})
	if err == nil {
		return errors.New("S06 非法 TTL 未被 SDK 拒绝")
	}
	_, err = sandbox.StartExecution(ctx, sdk.ExecuteRequest{
		Argv:    []string{"true"},
		Timeout: 1500 * time.Millisecond,
	})
	if err == nil {
		return errors.New("S06 非整秒 timeout 未被 SDK 拒绝")
	}
	_, err = client.Create(ctx, createRequest, sdk.WithIdempotencyKey("invalid key"))
	if err == nil {
		return errors.New("S06 非法幂等 key 未被 SDK 拒绝")
	}
	pass("S06", "ResponseError、Context 和本地参数校验正确", "")

	terminatedInfo, err := sandbox.DeleteAndWait(ctx)
	if err != nil {
		return fmt.Errorf("S07 删除并等待 Terminated: %w", err)
	}
	if terminatedInfo.State != sdk.SandboxStateTerminated {
		return fmt.Errorf("S07 非预期终态: %s", terminatedInfo.State)
	}
	if err := sandbox.Delete(ctx); err != nil {
		return fmt.Errorf("S07 重复删除: %w", err)
	}
	_, err = sandbox.StartExecution(ctx, sdk.ExecuteRequest{
		Argv: []string{"/bin/true"},
	})
	if err := expectResponseError(err, http.StatusConflict, "SANDBOX_NOT_RUNNING"); err != nil {
		return fmt.Errorf("S07 终态拒绝 execution: %w", err)
	}
	cleanupNeeded = false
	pass("S07", "删除收敛到 Terminated、重复删除且拒绝新执行", sandbox.ID())
	return nil
}

func expectResponseError(err error, status int, code string) error {
	if err == nil {
		return fmt.Errorf("got nil, want HTTP %d", status)
	}
	var responseError *sdk.ResponseError
	if !errors.As(err, &responseError) {
		return fmt.Errorf("got %T %v, want *sdk.ResponseError", err, err)
	}
	if responseError.StatusCode != status {
		return fmt.Errorf("got HTTP %d, want %d", responseError.StatusCode, status)
	}
	if code != "" && responseError.Detail.Code != code {
		return fmt.Errorf("got error code %q, want %q", responseError.Detail.Code, code)
	}
	if responseError.Detail.RequestID == "" {
		return errors.New("响应错误缺少 request_id")
	}
	return nil
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func pass(id, description, detail string) {
	if detail == "" {
		fmt.Printf("PASS %-3s %s\n", id, description)
		return
	}
	fmt.Printf("PASS %-3s %s (%s)\n", id, description, detail)
}
