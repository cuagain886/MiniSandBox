//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
)

// TestSuperviseRunnerUsesSingleDrainLoop 验证一次 SIGCHLD 会排空多名 child，
// runner status 与孤儿计数互不混淆，且每个 PID 只消费一次。
func TestSuperviseRunnerUsesSingleDrainLoop(t *testing.T) {
	const runnerPID = 42
	type response struct {
		pid    int
		status syscall.WaitStatus
		err    error
	}
	responses := []response{
		{pid: 99, status: exitedStatus(3)},
		{pid: runnerPID, status: exitedStatus(7)},
		{pid: 0},
	}
	calls := 0
	wait4 := func(_ int, status *syscall.WaitStatus, options int, _ *syscall.Rusage) (int, error) {
		if options != syscall.WNOHANG {
			t.Fatalf("wait4 options: got %d, want WNOHANG", options)
		}
		response := responses[calls]
		calls++
		*status = response.status
		return response.pid, response.err
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGCHLD
	result, err := superviseRunner(runnerPID, signals, wait4)
	if err != nil {
		t.Fatalf("supervise runner: %v", err)
	}
	if calls != len(responses) || result.orphanCount != 1 || !result.runnerStatus.Exited() || result.runnerStatus.ExitStatus() != 7 {
		t.Fatalf("unexpected reap result: calls=%d result=%+v", calls, result)
	}
}

// TestDrainChildrenHandlesECHILDAndEINTR 验证 wait4 中断可重试，而未记录
// runner status 的 ECHILD 必须作为内部错误返回。
func TestDrainChildrenHandlesECHILDAndEINTR(t *testing.T) {
	calls := 0
	wait4 := func(_ int, _ *syscall.WaitStatus, _ int, _ *syscall.Rusage) (int, error) {
		calls++
		if calls == 1 {
			return -1, syscall.EINTR
		}
		return -1, syscall.ECHILD
	}
	if _, err := drainChildren(42, wait4, &reapResult{}); err == nil {
		t.Fatal("expected missing runner status error")
	}
}

func exitedStatus(code int) syscall.WaitStatus {
	return syscall.WaitStatus(code << 8)
}
