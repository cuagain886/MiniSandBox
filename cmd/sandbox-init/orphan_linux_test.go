//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	prSetChildSubreaper = 36
	orphanTestModeKey   = "MINISANDBOX_INIT_ORPHAN_MODE"
	orphanExpectedPPID  = "MINISANDBOX_INIT_ORPHAN_EXPECTED_PPID"
)

// TestSandboxInitReapsDoubleForkOrphan 在独立 Linux subreaper 进程中验证两级
// 父进程退出后的 orphan 先重挂、形成 zombie，再被唯一 wait4 循环回收。
func TestSandboxInitReapsDoubleForkOrphan(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSandboxInitOrphanSupervisorHelper")
	command.Env = helperEnvironment("supervisor", "")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("orphan integration helper: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "orphan-reaped\n") {
		t.Fatalf("unexpected orphan integration evidence: %q", output)
	}
}

// TestSandboxInitOrphanSupervisorHelper 是隔离 subreaper 与生产 supervise loop
// 的子进程入口，避免它的 wait4(-1) 与 go test 主进程的其他 child 竞争。
func TestSandboxInitOrphanSupervisorHelper(t *testing.T) {
	if os.Getenv(orphanTestModeKey) != "supervisor" {
		return
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatalf("set child subreaper: %v", errno)
	}

	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create report pipe: %v", err)
	}
	orphanReleaseRead, orphanReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create orphan release pipe: %v", err)
	}
	runnerReleaseRead, runnerReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create runner release pipe: %v", err)
	}
	defer reportRead.Close()
	defer orphanReleaseWrite.Close()
	defer runnerReleaseWrite.Close()

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGCHLD)
	defer signal.Stop(signals)

	runner := exec.Command(os.Args[0], "-test.run=TestSandboxInitOrphanRunnerHelper")
	runner.Env = helperEnvironment("runner", strconv.Itoa(os.Getpid()))
	runner.ExtraFiles = []*os.File{reportWrite, orphanReleaseRead, runnerReleaseRead}
	runner.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := runner.Start(); err != nil {
		t.Fatalf("start runner helper: %v", err)
	}
	_ = reportWrite.Close()
	_ = orphanReleaseRead.Close()
	_ = runnerReleaseRead.Close()

	line, err := bufio.NewReader(reportRead).ReadString('\n')
	if err != nil {
		t.Fatalf("read orphan evidence: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		t.Fatalf("invalid orphan evidence: %q", line)
	}
	orphanPID, err := strconv.Atoi(fields[0])
	if err != nil || fields[1] != strconv.Itoa(os.Getpid()) {
		t.Fatalf("orphan was not reparented to supervisor: %q", line)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d/status", orphanPID)); err != nil {
		t.Fatalf("orphan proc entry missing before release: %v", err)
	}

	if _, err := orphanReleaseWrite.Write([]byte{1}); err != nil {
		t.Fatalf("release orphan: %v", err)
	}
	if err := waitForProcState(orphanPID, "Z", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerReleaseWrite.Write([]byte{1}); err != nil {
		t.Fatalf("release runner: %v", err)
	}

	result, err := superviseRunner(runner.Process.Pid, signals, syscall.Wait4, syscall.Kill)
	if err != nil {
		t.Fatalf("supervise runner: %v", err)
	}
	if code, err := runnerExitCode(result.runnerStatus); err != nil || code != 0 {
		t.Fatalf("runner status: %+v, code=%d, err=%v", result.runnerStatus, code, err)
	}
	if result.orphanCount < 2 {
		t.Fatalf("reaped orphan count: got %d, want at least 2", result.orphanCount)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d/status", orphanPID)); !os.IsNotExist(err) {
		t.Fatalf("orphan proc entry remains after reap: %v", err)
	}
	_, _ = os.Stdout.WriteString("orphan-reaped\n")
}

// TestSandboxInitOrphanRunnerHelper 启动中间父进程后保持 runner 存活，证明
// init 可以先回收 orphan 并继续等待 runner。
func TestSandboxInitOrphanRunnerHelper(t *testing.T) {
	if os.Getenv(orphanTestModeKey) != "runner" {
		return
	}
	report := os.NewFile(3, "orphan-report")
	orphanRelease := os.NewFile(4, "orphan-release")
	runnerRelease := os.NewFile(5, "runner-release")
	middle := exec.Command(os.Args[0], "-test.run=TestSandboxInitOrphanMiddleHelper")
	middle.Env = helperEnvironment("middle", os.Getenv(orphanExpectedPPID))
	middle.ExtraFiles = []*os.File{report, orphanRelease, runnerRelease}
	if err := middle.Start(); err != nil {
		os.Exit(11)
	}
	_ = report.Close()
	_ = orphanRelease.Close()
	var one [1]byte
	if _, err := runnerRelease.Read(one[:]); err != nil {
		os.Exit(12)
	}
}

// TestSandboxInitOrphanMiddleHelper 创建最终 orphan 后立即退出且不调用 Wait。
func TestSandboxInitOrphanMiddleHelper(t *testing.T) {
	if os.Getenv(orphanTestModeKey) != "middle" {
		return
	}
	orphan := exec.Command(os.Args[0], "-test.run=TestSandboxInitOrphanLeafHelper")
	orphan.Env = helperEnvironment("leaf", os.Getenv(orphanExpectedPPID))
	orphan.ExtraFiles = []*os.File{
		os.NewFile(3, "orphan-report"),
		os.NewFile(4, "orphan-release"),
		os.NewFile(5, "runner-release"),
	}
	if err := orphan.Start(); err != nil {
		os.Exit(21)
	}
}

// TestSandboxInitOrphanLeafHelper 等待内核完成重挂，报告 `/proc` 可核对的
// PID/PPID 后阻塞到 supervisor 明确允许退出。
func TestSandboxInitOrphanLeafHelper(t *testing.T) {
	if os.Getenv(orphanTestModeKey) != "leaf" {
		return
	}
	expected, err := strconv.Atoi(os.Getenv(orphanExpectedPPID))
	if err != nil {
		os.Exit(31)
	}
	deadline := time.Now().Add(5 * time.Second)
	for os.Getppid() != expected {
		if time.Now().After(deadline) {
			os.Exit(32)
		}
		runtime.Gosched()
	}
	report := os.NewFile(3, "orphan-report")
	orphanRelease := os.NewFile(4, "orphan-release")
	_ = os.NewFile(5, "runner-release").Close()
	_, _ = fmt.Fprintf(report, "%d %d\n", os.Getpid(), os.Getppid())
	_ = report.Close()
	var one [1]byte
	if _, err := orphanRelease.Read(one[:]); err != nil {
		os.Exit(33)
	}
}

func waitForProcState(pid int, want string, timeout time.Duration) error {
	path := fmt.Sprintf("/proc/%d/status", pid)
	deadline := time.Now().Add(timeout)
	for {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read orphan proc status: %w", err)
		}
		for _, line := range strings.Split(string(content), "\n") {
			if strings.HasPrefix(line, "State:") && strings.Contains(line, "\t"+want+" ") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("orphan PID %d did not reach proc state %s", pid, want)
		}
		runtime.Gosched()
	}
}

func helperEnvironment(mode, expectedPPID string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, orphanTestModeKey+"=") && !strings.HasPrefix(entry, orphanExpectedPPID+"=") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, orphanTestModeKey+"="+mode)
	if expectedPPID != "" {
		environment = append(environment, orphanExpectedPPID+"="+expectedPPID)
	}
	return environment
}
