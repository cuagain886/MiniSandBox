//go:build linux

package runner

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"minisandbox/internal/runnerbootstrap"
)

const privilegeHelperEnv = "MINISANDBOX_RUNNER_PRIVILEGE_HELPER"

// TestDropPrivilegesCallOrder 验证 keepcaps 关闭后，身份系统调用严格保持
// setgroups → setgid → setuid，且任一步失败不继续执行。
func TestDropPrivilegesCallOrder(t *testing.T) {
	identity := runnerbootstrap.Identity{ExecutionUID: 2000, ExecutionGID: 2001, SocketOwnerUID: 0, SocketOwnerGID: 0}
	steps := []string{}
	ops := identityOps{
		disableKeepCaps: func() error { steps = append(steps, "keepcaps=0"); return nil },
		setgroups: func(groups []int) error {
			if len(groups) != 0 {
				t.Fatalf("supplementary groups not empty: %v", groups)
			}
			steps = append(steps, "setgroups")
			return nil
		},
		setgid: func(gid int) error { steps = append(steps, "setgid="+strconv.Itoa(gid)); return nil },
		setuid: func(uid int) error { steps = append(steps, "setuid="+strconv.Itoa(uid)); return nil },
	}
	if err := dropPrivileges(identity, ops); err != nil {
		t.Fatalf("drop privileges: %v", err)
	}
	want := "keepcaps=0,setgroups,setgid=2001,setuid=2000"
	if got := strings.Join(steps, ","); got != want {
		t.Fatalf("call order: got %s, want %s", got, want)
	}

	for _, failedStep := range []string{"keepcaps", "setgroups", "setgid", "setuid"} {
		t.Run("fail-"+failedStep, func(t *testing.T) {
			calls := 0
			fail := errors.New("injected failure")
			operation := func(name string) error {
				calls++
				if name == failedStep {
					return fail
				}
				return nil
			}
			err := dropPrivileges(identity, identityOps{
				disableKeepCaps: func() error { return operation("keepcaps") },
				setgroups:       func([]int) error { return operation("setgroups") },
				setgid:          func(int) error { return operation("setgid") },
				setuid:          func(int) error { return operation("setuid") },
			})
			if !errors.Is(err, fail) {
				t.Fatalf("failure not returned: %v", err)
			}
			wantCalls := map[string]int{"keepcaps": 1, "setgroups": 2, "setgid": 3, "setuid": 4}[failedStep]
			if calls != wantCalls {
				t.Fatalf("calls after failure: got %d, want %d", calls, wantCalls)
			}
		})
	}
}

// TestDropPrivilegesRejectsInvalidIdentity 验证 root identity 和 socket owner
// 重合均在任何系统调用前被拒绝。
func TestDropPrivilegesRejectsInvalidIdentity(t *testing.T) {
	tests := []runnerbootstrap.Identity{
		{ExecutionUID: 0, ExecutionGID: 1000, SocketOwnerUID: 2000, SocketOwnerGID: 2000},
		{ExecutionUID: 1000, ExecutionGID: 0, SocketOwnerUID: 2000, SocketOwnerGID: 2000},
		{ExecutionUID: 1000, ExecutionGID: 1001, SocketOwnerUID: 1000, SocketOwnerGID: 2000},
		{ExecutionUID: 1000, ExecutionGID: 1001, SocketOwnerUID: 2000, SocketOwnerGID: 1001},
	}
	for _, identity := range tests {
		if err := dropPrivileges(identity, identityOps{
			disableKeepCaps: func() error { t.Fatal("system call reached"); return nil },
		}); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}

// TestDropPrivilegesRootHelper 在 root Linux 测试环境验证降权后无法恢复 root、
// execution UID 无法 pathname connect，而 listener fd 仍能 accept owner 连接。
func TestDropPrivilegesRootHelper(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root Linux helper; run in the privileged integration environment")
	}
	command := exec.Command(os.Args[0], "-test.run=TestDropPrivilegesChild")
	command.Env = append(os.Environ(), privilegeHelperEnv+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("child stdout: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	reader := bufio.NewReader(stdout)
	socketPath, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read socket path: %v", err)
	}
	socketPath = strings.TrimSpace(socketPath)
	connection, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		t.Fatalf("owner dial after child drop: %v", err)
	}
	_ = connection.Close()
	evidence, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(evidence) != "dropped-no-restore" {
		t.Fatalf("child evidence: %q %v", evidence, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
}

// TestDropPrivilegesChild 是 root helper 的隔离子进程入口。
func TestDropPrivilegesChild(t *testing.T) {
	if os.Getenv(privilegeHelperEnv) != "1" {
		return
	}
	directory := filepath.Join(os.TempDir(), "minisandbox-drop-"+strconv.Itoa(os.Getpid()))
	if err := os.Mkdir(directory, 0o700); err != nil {
		os.Exit(10)
	}
	identity := runnerbootstrap.Identity{ExecutionUID: 65534, ExecutionGID: 65534, SocketOwnerUID: 0, SocketOwnerGID: 0}
	listener, err := bindManagedSocketAt(directory, filepath.Join(directory, "runner.sock"), identity)
	if err != nil {
		os.Exit(11)
	}
	if err := DropPrivileges(listener, identity); err != nil {
		os.Exit(12)
	}
	if os.Geteuid() != 65534 || os.Getegid() != 65534 {
		os.Exit(13)
	}
	groups, err := syscall.Getgroups()
	if err != nil || len(groups) != 0 {
		os.Exit(14)
	}
	if connection, err := net.DialTimeout("unix", filepath.Join(directory, "runner.sock"), 100*time.Millisecond); err == nil {
		_ = connection.Close()
		os.Exit(15)
	}
	_, _ = os.Stdout.WriteString(filepath.Join(directory, "runner.sock") + "\n")
	connection, err := listener.Accept()
	if err != nil {
		os.Exit(16)
	}
	_ = connection.Close()
	if err := syscall.Setuid(0); err == nil || os.Geteuid() == 0 {
		os.Exit(17)
	}
	_, _ = os.Stdout.WriteString("dropped-no-restore\n")
}
