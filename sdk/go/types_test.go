package sdk

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minisandbox/pkg/protocol"
)

// TestSDKStateEnumsMatchProtocol 验证 SDK 公开的状态与事件常量与公共协议
// 保持同一 identity，普通调用方无需导入 pkg/protocol 即可比较状态。
func TestSDKStateEnumsMatchProtocol(t *testing.T) {
	if SandboxStateRunning != protocol.SandboxStateRunning ||
		SandboxStateTerminated != protocol.SandboxStateTerminated ||
		SandboxStatePending != protocol.SandboxStatePending ||
		SandboxStateCreating != protocol.SandboxStateCreating ||
		SandboxStateStopping != protocol.SandboxStateStopping ||
		SandboxStateFailed != protocol.SandboxStateFailed {
		t.Fatal("sandbox state constants diverge from protocol")
	}
	if ExecutionStateExited != protocol.ExecutionStateExited ||
		ExecutionStateRunning != protocol.ExecutionStateRunning ||
		ExecutionStatePending != protocol.ExecutionStatePending ||
		ExecutionStateFailed != protocol.ExecutionStateFailed ||
		ExecutionStateCancelled != protocol.ExecutionStateCancelled ||
		ExecutionStateTimedOut != protocol.ExecutionStateTimedOut {
		t.Fatal("execution state constants diverge from protocol")
	}
	if EventStdout != protocol.EventStdout ||
		EventStderr != protocol.EventStderr ||
		EventExited != protocol.EventExited ||
		EventFailed != protocol.EventFailed ||
		EventCancelled != protocol.EventCancelled ||
		EventTimedOut != protocol.EventTimedOut ||
		EventStarted != protocol.EventStarted ||
		EventOutputLimitReached != protocol.EventOutputLimitReached {
		t.Fatal("event type constants diverge from protocol")
	}
	var state SandboxState = "Running"
	if state != SandboxStateRunning {
		t.Fatalf("sandbox state should compare as string alias: %s", state)
	}
}

// TestNewExecutionEventDecodesPayload 验证 wire 事件到 SDK 事件的转换会解码
// Base64 输出并映射毫秒耗时与可选终止字段。
func TestNewExecutionEventDecodesPayload(t *testing.T) {
	exitCode := 3
	duration := int64(1500)
	truncated := true
	event, err := newExecutionEvent(protocol.ExecutionEvent{
		ExecutionID: "exec-1",
		Sequence:    2,
		Timestamp:   time.Unix(1000, 0).UTC(),
		Type:        protocol.EventStdout,
		DataBase64:  base64.StdEncoding.EncodeToString([]byte("chunk")),
	})
	if err != nil {
		t.Fatalf("decode stdout event: %v", err)
	}
	if string(event.Data) != "chunk" || event.ExitCode != 0 || event.Terminal() {
		t.Fatalf("unexpected decoded event: %#v", event)
	}

	terminal, err := newExecutionEvent(protocol.ExecutionEvent{
		ExecutionID:     "exec-1",
		Sequence:        3,
		Timestamp:       time.Unix(1001, 0).UTC(),
		Type:            protocol.EventExited,
		ExitCode:        &exitCode,
		DurationMS:      &duration,
		OutputTruncated: &truncated,
	})
	if err != nil {
		t.Fatalf("decode exited event: %v", err)
	}
	if !terminal.Terminal() || terminal.ExitCode != exitCode ||
		terminal.Duration != 1500*time.Millisecond || !terminal.OutputTruncated {
		t.Fatalf("unexpected terminal event: %#v", terminal)
	}

	if _, err := newExecutionEvent(protocol.ExecutionEvent{
		Sequence:   1,
		Type:       protocol.EventStdout,
		DataBase64: "not-base64!!",
	}); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// TestNewSandboxAndExecutionInfo 验证 wire 资源到 SDK 信息模型的字段映射。
func TestNewSandboxAndExecutionInfo(t *testing.T) {
	createdAt := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	sandboxInfo := newSandboxInfo(protocol.Sandbox{
		ID:        "sbx-1",
		State:     protocol.SandboxStateRunning,
		Reason:    protocol.SandboxReasonRunning,
		Message:   "ok",
		Image:     "debian:bookworm-slim",
		ExpiresAt: createdAt.Add(time.Hour),
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if sandboxInfo.ID != "sbx-1" || sandboxInfo.State != SandboxStateRunning ||
		sandboxInfo.ExpiresAt.Sub(sandboxInfo.CreatedAt) != time.Hour {
		t.Fatalf("unexpected sandbox info: %#v", sandboxInfo)
	}

	executionInfo, err := newExecutionInfo(protocol.ExecutionStatus{
		ExecutionID: "exec-1",
		State:       protocol.ExecutionStateRunning,
	})
	if err != nil {
		t.Fatalf("convert execution info: %v", err)
	}
	if executionInfo.TerminalEvent != nil {
		t.Fatalf("running execution should have no terminal event: %#v", executionInfo)
	}

	terminalInfo, err := newExecutionInfo(protocol.ExecutionStatus{
		ExecutionID: "exec-1",
		State:       protocol.ExecutionStateTimedOut,
		TerminalEvent: &protocol.ExecutionEvent{
			ExecutionID:     "exec-1",
			Sequence:        5,
			Type:            protocol.EventTimedOut,
			DurationMS:      &[]int64{200}[0],
			OutputTruncated: &[]bool{false}[0],
		},
	})
	if err != nil {
		t.Fatalf("convert terminal execution info: %v", err)
	}
	if terminalInfo.TerminalEvent == nil ||
		terminalInfo.TerminalEvent.Type != EventTimedOut ||
		terminalInfo.TerminalEvent.Duration != 200*time.Millisecond {
		t.Fatalf("unexpected terminal info: %#v", terminalInfo)
	}
}

// TestRunTerminalErrors 验证 Run 终态错误类型可被 errors.As 区分。
func TestRunTerminalErrors(t *testing.T) {
	exit := &ExitError{ExecutionID: "exec-1", ExitCode: 2}
	if exit.Error() == "" || exit.ExecutionID != "exec-1" || exit.ExitCode != 2 {
		t.Fatalf("unexpected exit error: %v", exit)
	}
	wrapped := errors.Join(exit, &ExecutionCancelledError{ExecutionID: "exec-1"})
	var exitErr *ExitError
	var cancelErr *ExecutionCancelledError
	if !errors.As(wrapped, &exitErr) || !errors.As(wrapped, &cancelErr) {
		t.Fatalf("terminal errors should support errors.As: %v", wrapped)
	}
	timeout := &ExecutionTimedOutError{ExecutionID: "exec-2"}
	failed := &ExecutionFailedError{ExecutionID: "exec-3", ErrorCode: "SPAWN_FAILED", Message: "spawn failed"}
	if timeout.Error() == "" || failed.Error() == "" {
		t.Fatal("terminal errors should render diagnostics")
	}
	var timeoutErr *ExecutionTimedOutError
	if !errors.As(error(timeout), &timeoutErr) || timeoutErr != timeout {
		t.Fatalf("timed-out error should support errors.As: %v", timeout)
	}
	var failedErr *ExecutionFailedError
	if !errors.As(error(failed), &failedErr) || failedErr != failed {
		t.Fatalf("failed error should support errors.As: %v", failed)
	}
}

// TestResponseErrorHelpers 验证 HTTP 错误 helper 的判断语义。
func TestResponseErrorHelpers(t *testing.T) {
	notFound := &ResponseError{StatusCode: 404}
	conflict := &ResponseError{StatusCode: 409, Detail: protocol.ErrorDetail{Retryable: true}}
	if !notFound.IsNotFound() || notFound.IsConflict() || notFound.IsRetryable() {
		t.Fatal("unexpected not-found classification")
	}
	if conflict.IsNotFound() || !conflict.IsConflict() || !conflict.IsRetryable() {
		t.Fatal("unexpected conflict classification")
	}
}

// ExampleClient_lowLevelAPI 编译并展示现有底层方法保持可用；高层接口在
// 后续阶段加入后由独立示例覆盖。
func ExampleClient_lowLevelAPI() {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sbx-example","state":"Pending"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())
	sandbox, err := client.GetSandbox(context.Background(), "sbx-example")
	if err != nil {
		return
	}
	if sandbox.State == SandboxStatePending {
		// SandboxStatePending 直接来自 sdk 包，无需导入 pkg/protocol。
		// Output: pending
		println("pending")
	}
}
