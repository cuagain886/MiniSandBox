// Package main 提供只依赖公开 Go SDK 的 MiniSandbox 端到端验收程序。
// 它验证调用方可见的核心闭环，不替代网络隔离、进程残留和崩溃恢复安全测试。
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"minisandbox/pkg/protocol"
	"minisandbox/sdk/go"
)

const (
	defaultBaseURL = "http://127.0.0.1:8080"
	defaultImage   = "debian:bookworm-slim"
	pollInterval   = 250 * time.Millisecond
	waitTimeout    = 90 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nSDK 验收失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n7/7 PASS：Go SDK 核心功能验收通过")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	baseURL := environmentOrDefault("MINISANDBOX_URL", defaultBaseURL)
	image := environmentOrDefault("MINISANDBOX_IMAGE", defaultImage)
	client := sdk.NewClient(baseURL, &http.Client{Timeout: 15 * time.Second})

	ttl := 10 * time.Minute
	createRequest := sdk.CreateSandboxRequest{Image: image, TTL: &ttl}
	idempotencyKey := fmt.Sprintf("sdk-acceptance-%d", time.Now().UTC().UnixNano())
	createOptions := sdk.CreateSandboxOptions{IdempotencyKey: idempotencyKey}

	fmt.Printf("MiniSandbox SDK 验收：server=%s image=%s\n", baseURL, image)
	sandbox, err := client.CreateSandboxWithOptions(ctx, createRequest, createOptions)
	if err != nil {
		return fmt.Errorf("S01 创建 sandbox: %w", err)
	}
	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.DeleteSandbox(cleanupCtx, sandbox.ID)
		}
	}()

	sandbox, err = waitSandboxState(ctx, client, sandbox.ID, protocol.SandboxStateRunning)
	if err != nil {
		return fmt.Errorf("S01 等待 Running: %w", err)
	}
	if sandbox.ExpiresAt.IsZero() || !sandbox.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("S01 sandbox expiry 无效: %s", sandbox.ExpiresAt)
	}
	pass("S01", "创建 sandbox 并进入 Running", sandbox.ID)

	replayed, err := client.CreateSandboxWithOptions(ctx, createRequest, createOptions)
	if err != nil {
		return fmt.Errorf("S02 幂等重放: %w", err)
	}
	if replayed.ID != sandbox.ID {
		return fmt.Errorf("S02 幂等重放创建了不同 sandbox: %s != %s", replayed.ID, sandbox.ID)
	}
	conflictingTTL := 11 * time.Minute
	_, err = client.CreateSandboxWithOptions(ctx, sdk.CreateSandboxRequest{
		Image: image,
		TTL:   &conflictingTTL,
	}, createOptions)
	if err := expectResponseError(err, http.StatusConflict, string(protocol.ErrorCodeIdempotencyConflict)); err != nil {
		return fmt.Errorf("S02 幂等冲突: %w", err)
	}
	pass("S02", "幂等重放返回同一资源，不同请求返回 409", sandbox.ID)

	execution, err := client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
		Argv: []string{
			"/bin/sh", "-c",
			"printf 'sdk-stdout'; printf 'sdk-stderr' >&2",
		},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S03 创建 execution: %w", err)
	}
	status, err := waitExecutionTerminal(ctx, client, sandbox.ID, execution.ExecutionID)
	if err != nil {
		return fmt.Errorf("S03 等待 execution 终态: %w", err)
	}
	if status.State != protocol.ExecutionStateExited ||
		status.TerminalEvent == nil ||
		status.TerminalEvent.Type != protocol.EventExited ||
		status.TerminalEvent.ExitCode == nil ||
		*status.TerminalEvent.ExitCode != 0 {
		return fmt.Errorf("S03 非预期终态: state=%s event=%#v", status.State, status.TerminalEvent)
	}
	stdout, stderr, err := readCompleteLogs(ctx, client, sandbox.ID, execution.ExecutionID)
	if err != nil {
		return fmt.Errorf("S03 读取日志: %w", err)
	}
	if stdout != "sdk-stdout" || stderr != "sdk-stderr" {
		return fmt.Errorf("S03 输出不匹配: stdout=%q stderr=%q", stdout, stderr)
	}
	pass("S03", "命令退出码、stdout/stderr 和日志游标正确", execution.ExecutionID)

	cancellable, err := client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
		Argv:    []string{"/bin/sh", "-c", "sleep 30 & wait"},
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("S04 创建待取消 execution: %w", err)
	}
	if err := waitExecutionRunning(ctx, client, sandbox.ID, cancellable.ExecutionID); err != nil {
		return fmt.Errorf("S04 等待 execution Running: %w", err)
	}
	if err := client.CancelExecution(ctx, sandbox.ID, cancellable.ExecutionID); err != nil {
		return fmt.Errorf("S04 取消 execution: %w", err)
	}
	cancelled, err := waitExecutionTerminal(ctx, client, sandbox.ID, cancellable.ExecutionID)
	if err != nil {
		return fmt.Errorf("S04 等待取消终态: %w", err)
	}
	if cancelled.State != protocol.ExecutionStateCancelled ||
		cancelled.TerminalEvent == nil ||
		cancelled.TerminalEvent.Type != protocol.EventCancelled {
		return fmt.Errorf("S04 非预期取消终态: state=%s event=%#v", cancelled.State, cancelled.TerminalEvent)
	}
	if err := client.CancelExecution(ctx, sandbox.ID, cancellable.ExecutionID); err != nil {
		return fmt.Errorf("S04 重复取消: %w", err)
	}
	pass("S04", "长任务取消并保持稳定 Cancelled 终态", cancellable.ExecutionID)

	previousExpiry := sandbox.ExpiresAt
	requestedExpiry := previousExpiry.Add(5 * time.Minute)
	renewed, err := client.RenewSandbox(ctx, sandbox.ID, sdk.RenewSandboxRequest{ExpiresAt: requestedExpiry})
	if err != nil {
		return fmt.Errorf("S05 续期: %w", err)
	}
	if !renewed.ExpiresAt.Equal(requestedExpiry) {
		return fmt.Errorf("S05 续期结果不匹配: got=%s want=%s", renewed.ExpiresAt, requestedExpiry)
	}
	replayedRenew, err := client.RenewSandbox(ctx, sandbox.ID, sdk.RenewSandboxRequest{ExpiresAt: requestedExpiry})
	if err != nil || !replayedRenew.ExpiresAt.Equal(requestedExpiry) {
		return fmt.Errorf("S05 续期重放: expiry=%s err=%w", replayedRenew.ExpiresAt, err)
	}
	_, err = client.RenewSandbox(ctx, sandbox.ID, sdk.RenewSandboxRequest{ExpiresAt: previousExpiry})
	if err := expectResponseError(err, http.StatusConflict, string(protocol.ErrorCodeLeaseConflict)); err != nil {
		return fmt.Errorf("S05 拒绝缩短租约: %w", err)
	}
	sandbox = renewed
	pass("S05", "续期、幂等重放和拒绝缩短租约", sandbox.ExpiresAt.Format(time.RFC3339))

	_, err = client.GetSandbox(ctx, "00000000-0000-4000-8000-000000000000")
	if err := expectResponseError(err, http.StatusNotFound, ""); err != nil {
		return fmt.Errorf("S06 404 错误模型: %w", err)
	}
	cancelledContext, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	_, err = client.GetSandbox(cancelledContext, sandbox.ID)
	if !errors.Is(err, context.Canceled) {
		return fmt.Errorf("S06 context 取消: got %v, want context.Canceled", err)
	}
	invalidTTL := 30 * time.Second
	_, err = client.CreateSandboxWithOptions(ctx, sdk.CreateSandboxRequest{
		Image: image,
		TTL:   &invalidTTL,
	}, sdk.CreateSandboxOptions{})
	if err == nil {
		return errors.New("S06 非法 TTL 未被 SDK 拒绝")
	}
	_, err = client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
		Argv:    []string{"true"},
		Timeout: 1500 * time.Millisecond,
	})
	if err == nil {
		return errors.New("S06 非整秒 timeout 未被 SDK 拒绝")
	}
	_, err = client.CreateSandboxWithOptions(ctx, createRequest, sdk.CreateSandboxOptions{
		IdempotencyKey: "invalid key",
	})
	if err == nil {
		return errors.New("S06 非法幂等 key 未被 SDK 拒绝")
	}
	pass("S06", "ResponseError、Context 和本地参数校验正确", "")

	if err := client.DeleteSandbox(ctx, sandbox.ID); err != nil {
		return fmt.Errorf("S07 删除 sandbox: %w", err)
	}
	sandbox, err = waitSandboxState(ctx, client, sandbox.ID, protocol.SandboxStateTerminated)
	if err != nil {
		return fmt.Errorf("S07 等待 Terminated: %w", err)
	}
	if err := client.DeleteSandbox(ctx, sandbox.ID); err != nil {
		return fmt.Errorf("S07 重复删除: %w", err)
	}
	_, err = client.StartBackgroundExecution(ctx, sandbox.ID, sdk.ExecuteRequest{
		Argv: []string{"/bin/true"},
	})
	if err := expectResponseError(err, http.StatusConflict, string(protocol.ErrorCodeSandboxNotRunning)); err != nil {
		return fmt.Errorf("S07 终态拒绝 execution: %w", err)
	}
	cleanupNeeded = false
	pass("S07", "删除收敛到 Terminated、重复删除且拒绝新执行", sandbox.ID)
	return nil
}

func waitSandboxState(
	ctx context.Context,
	client *sdk.Client,
	sandboxID string,
	want protocol.SandboxState,
) (protocol.Sandbox, error) {
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		sandbox, err := client.GetSandbox(ctx, sandboxID)
		if err != nil {
			return protocol.Sandbox{}, err
		}
		if sandbox.State == want {
			return sandbox, nil
		}
		if sandbox.State == protocol.SandboxStateFailed ||
			(want == protocol.SandboxStateRunning && sandbox.State == protocol.SandboxStateTerminated) {
			return protocol.Sandbox{}, fmt.Errorf(
				"sandbox 提前进入终态: state=%s reason=%s message=%s",
				sandbox.State,
				sandbox.Reason,
				sandbox.Message,
			)
		}
		select {
		case <-ctx.Done():
			return protocol.Sandbox{}, ctx.Err()
		case <-deadline.C:
			return protocol.Sandbox{}, fmt.Errorf("等待 sandbox %s 超时", want)
		case <-ticker.C:
		}
	}
}

func waitExecutionRunning(ctx context.Context, client *sdk.Client, sandboxID, executionID string) error {
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		status, err := client.GetExecution(ctx, sandboxID, executionID)
		if err != nil {
			return err
		}
		if status.State == protocol.ExecutionStateRunning {
			return nil
		}
		if executionTerminal(status.State) {
			return fmt.Errorf("execution 在取消前已进入终态 %s", status.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("等待 execution Running 超时")
		case <-ticker.C:
		}
	}
}

func waitExecutionTerminal(
	ctx context.Context,
	client *sdk.Client,
	sandboxID string,
	executionID string,
) (protocol.ExecutionStatus, error) {
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		status, err := client.GetExecution(ctx, sandboxID, executionID)
		if err != nil {
			return protocol.ExecutionStatus{}, err
		}
		if executionTerminal(status.State) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return protocol.ExecutionStatus{}, ctx.Err()
		case <-deadline.C:
			return protocol.ExecutionStatus{}, errors.New("等待 execution 终态超时")
		case <-ticker.C:
		}
	}
}

func readCompleteLogs(
	ctx context.Context,
	client *sdk.Client,
	sandboxID string,
	executionID string,
) (string, string, error) {
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	var stdout, stderr strings.Builder
	var cursor, lastSequence uint64

	for {
		page, err := client.GetExecutionLogs(ctx, sandboxID, executionID, cursor)
		if err != nil {
			return "", "", err
		}
		for _, event := range page.Events {
			if err := event.Validate(); err != nil {
				return "", "", fmt.Errorf("非法 execution event: sequence=%d type=%s", event.Sequence, event.Type)
			}
			if event.Sequence <= lastSequence {
				return "", "", fmt.Errorf("event sequence 未严格递增: %d <= %d", event.Sequence, lastSequence)
			}
			lastSequence = event.Sequence
			if event.Type != protocol.EventStdout && event.Type != protocol.EventStderr {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil {
				return "", "", fmt.Errorf("解码 %s: %w", event.Type, err)
			}
			if event.Type == protocol.EventStdout {
				_, _ = stdout.Write(data)
			} else {
				_, _ = stderr.Write(data)
			}
		}
		if page.NextCursor < cursor || (len(page.Events) > 0 && page.NextCursor != lastSequence) {
			return "", "", fmt.Errorf("非法日志游标: cursor=%d next=%d last=%d", cursor, page.NextCursor, lastSequence)
		}
		cursor = page.NextCursor
		if page.Complete {
			return stdout.String(), stderr.String(), nil
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-deadline.C:
			return "", "", errors.New("等待完整日志超时")
		case <-time.After(pollInterval):
		}
	}
}

func executionTerminal(state protocol.ExecutionState) bool {
	switch state {
	case protocol.ExecutionStateExited,
		protocol.ExecutionStateFailed,
		protocol.ExecutionStateCancelled,
		protocol.ExecutionStateTimedOut:
		return true
	default:
		return false
	}
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
