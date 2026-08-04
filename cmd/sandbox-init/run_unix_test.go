//go:build unix

package main

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	result, err := superviseRunner(runnerPID, signals, wait4, func(int, syscall.Signal) error { return nil })
	if err != nil {
		t.Fatalf("supervise runner: %v", err)
	}
	if calls != len(responses) || result.orphanCount != 1 || !result.runnerStatus.Exited() || result.runnerStatus.ExitStatus() != 7 {
		t.Fatalf("unexpected reap result: calls=%d result=%+v", calls, result)
	}
}

// TestForwardRunnerSignalTargetsOnlyRunnerGroup 验证三种允许信号使用负 PGID，
// SIGCHLD 和其他信号不转发，runner 先退出产生的 ESRCH 被忽略。
func TestForwardRunnerSignalTargetsOnlyRunnerGroup(t *testing.T) {
	for _, value := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		called := false
		err := forwardRunnerSignal(42, value, func(pid int, got syscall.Signal) error {
			called = true
			if pid != -42 || got != value {
				t.Fatalf("kill target: pid=%d signal=%d", pid, got)
			}
			return syscall.ESRCH
		})
		if err != nil || !called {
			t.Fatalf("forward %d: called=%t err=%v", value, called, err)
		}
	}
	for _, value := range []syscall.Signal{syscall.SIGCHLD, syscall.SIGUSR1} {
		if err := forwardRunnerSignal(42, value, func(int, syscall.Signal) error {
			t.Fatal("non-lifecycle signal was forwarded")
			return nil
		}); err != nil {
			t.Fatalf("ignore signal %d: %v", value, err)
		}
	}
	if err := forwardRunnerSignal(0, syscall.SIGTERM, syscall.Kill); err == nil {
		t.Fatal("invalid runner PID accepted")
	}
}

// TestForwardRunnerSignalsToHelperProcessGroup 在真实 Linux 进程组中证明 helper
// 能分别收到 TERM、INT 与 HUP，且目标不是测试进程本身。
func TestForwardRunnerSignalsToHelperProcessGroup(t *testing.T) {
	for _, value := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(strconv.Itoa(int(value)), func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=TestSandboxInitSignalHelper")
			command.Env = append(os.Environ(), "MINISANDBOX_INIT_SIGNAL_HELPER=1")
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatalf("helper stdout: %v", err)
			}
			if err := command.Start(); err != nil {
				t.Fatalf("start helper: %v", err)
			}
			reader := bufio.NewReader(stdout)
			ready, err := reader.ReadString('\n')
			if err != nil || strings.TrimSpace(ready) != "ready" {
				t.Fatalf("helper readiness: %q %v", ready, err)
			}
			if err := forwardRunnerSignal(command.Process.Pid, value, syscall.Kill); err != nil {
				t.Fatalf("forward signal: %v", err)
			}
			seen, err := reader.ReadString('\n')
			if err != nil || strings.TrimSpace(seen) != strconv.Itoa(int(value)) {
				t.Fatalf("helper signal: %q %v", seen, err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("wait helper: %v", err)
			}
		})
	}
}

// TestSandboxInitSignalHelper 是真实信号转发测试使用的隔离子进程入口。
func TestSandboxInitSignalHelper(t *testing.T) {
	if os.Getenv("MINISANDBOX_INIT_SIGNAL_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	_, _ = os.Stdout.WriteString("ready\n")
	select {
	case received := <-signals:
		value := received.(syscall.Signal)
		_, _ = os.Stdout.WriteString(strconv.Itoa(int(value)) + "\n")
	case <-time.After(5 * time.Second):
		os.Exit(3)
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

// TestRunnerExitCode 验证正常、非零和信号终态的稳定 Docker 退出码映射。
func TestRunnerExitCode(t *testing.T) {
	tests := []struct {
		name   string
		status syscall.WaitStatus
		want   int
	}{
		{name: "zero", status: exitedStatus(0), want: 0},
		{name: "nonzero", status: exitedStatus(23), want: 23},
		{name: "sigterm", status: signaledStatus(syscall.SIGTERM), want: 143},
		{name: "sigkill", status: signaledStatus(syscall.SIGKILL), want: 137},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := runnerExitCode(test.status)
			if err != nil || got != test.want {
				t.Fatalf("exit code: got %d, want %d, err %v", got, test.want, err)
			}
		})
	}
	if _, err := runnerExitCode(syscall.WaitStatus(0x7f)); err == nil {
		t.Fatal("non-terminal stopped status accepted")
	}
}

// TestRunMapsMissingRunnerTo127 验证 runner exec 启动失败不会与内部 init 错误混淆。
func TestRunMapsMissingRunnerTo127(t *testing.T) {
	if got := run([]string{"/definitely/missing/minisandbox-runnerd"}); got != 127 {
		t.Fatalf("missing runner exit code: got %d, want 127", got)
	}
}

func exitedStatus(code int) syscall.WaitStatus {
	return syscall.WaitStatus(code << 8)
}

func signaledStatus(value syscall.Signal) syscall.WaitStatus {
	return syscall.WaitStatus(value)
}
