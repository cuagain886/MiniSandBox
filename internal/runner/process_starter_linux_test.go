//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const parentDeathHelperEnv = "MINISANDBOX_TEST_PROCESS_STARTER_PARENT_DEATH"

// TestStartCommandCreatesIndependentProcessGroup 验证成功启动的 leader PID 与内核 PGID 完全相等。
func TestStartCommandCreatesIndependentProcessGroup(t *testing.T) {
	spec := buildLinuxCommandSpec(t, []string{"/bin/sleep", "30"})
	started, err := StartCommand(spec)
	if err != nil {
		t.Fatalf("start command: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-started.PGID, syscall.SIGKILL)
		_ = started.Command.Wait()
		started.Stdout.Close()
		started.Stderr.Close()
	})
	pgid, err := syscall.Getpgid(started.PID)
	if err != nil {
		t.Fatalf("get pgid: %v", err)
	}
	if started.PID <= 0 || started.PGID != started.PID || pgid != started.PID {
		t.Fatalf("process identity: PID=%d PGID=%d kernel=%d", started.PID, started.PGID, pgid)
	}
	if started.Command.SysProcAttr == nil || !started.Command.SysProcAttr.Setpgid || started.Command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("process safety attributes: %#v", started.Command.SysProcAttr)
	}
}

// TestStartCommandRejectsMissingAndPermissionDenied 验证 exec 失败不返回 started process 或遗留可等待 child。
func TestStartCommandRejectsMissingAndPermissionDenied(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	for _, argv := range [][]string{
		{"definitely-not-a-minisandbox-command"},
		{plain},
	} {
		spec := buildLinuxCommandSpec(t, argv)
		started, err := StartCommand(spec)
		if !errors.Is(err, ErrProcessStartFailed) || started.Command != nil || started.PID != 0 || started.PGID != 0 {
			t.Fatalf("argv %v: started=%+v err=%v", argv, started, err)
		}
		if spec.Command.Process != nil {
			t.Fatalf("failed start retained process: %+v", spec.Command.Process)
		}
	}
}

// TestProcessStarterParentDeathSafety 在独立 helper 退出后验证其直接 child 被内核 Pdeathsig 终止。
func TestProcessStarterParentDeathSafety(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestProcessStarterParentDeathHelper$")
	command.Env = append(os.Environ(), parentDeathHelperEnv+"=1")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run parent helper: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		t.Fatalf("helper child PID: output=%q err=%v", output, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processRunning(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("Pdeathsig child %d remained running", pid)
	}
}

// TestProcessStarterParentDeathHelper 是 parent-death 场景的子进程入口，不在普通测试进程中执行断言。
func TestProcessStarterParentDeathHelper(t *testing.T) {
	if os.Getenv(parentDeathHelperEnv) != "1" {
		return
	}
	spec := buildLinuxCommandSpec(t, []string{"/bin/sleep", "30"})
	started, err := StartCommand(spec)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, started.PID)
	_ = os.Stdout.Sync()
	os.Exit(0)
}

func buildLinuxCommandSpec(t *testing.T, argv []string) CommandSpec {
	t.Helper()
	builder := newCommandBuilder(func() (string, error) { return "/bin/sh", nil })
	spec, err := builder.Build(ValidatedRequest{Argv: argv, Timeout: time.Minute}, os.TempDir(), nil)
	if err != nil {
		t.Fatalf("build command spec: %v", err)
	}
	return spec
}

func processRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return false
	}
	if err == nil {
		closing := strings.LastIndexByte(string(data), ')')
		if closing >= 0 && closing+2 < len(data) && data[closing+2] == 'Z' {
			return false
		}
	}
	return syscall.Kill(pid, 0) == nil
}
